package form

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/ui/grid"
)

// lookup is the foreign-key lookup window (spec 11): a grid of the
// referenced table with all columns, a server-side quick-search filter
// (limited, lazily loaded); Enter fills all columns of the foreign key.
type lookup struct {
	f      *Form
	fk     *meta.ForeignKey
	ref    *meta.Table
	box    *tview.Box
	filter *tview.InputField
	grid   *grid.Grid
	res    *exec.Result
	status string
	isErr  bool

	inFlight, pending, closed bool
	queries                   int // number of queries run (tests)
}

func (f *Form) openLookup(fk *meta.ForeignKey) {
	if fk == nil {
		return
	}
	if f.opts.Backend == nil {
		f.setMsg("No connection", f.theme.Error)
		return
	}
	lk := &lookup{f: f, fk: fk, box: tview.NewBox(), filter: tview.NewInputField()}
	lk.box.SetBorder(true).SetBorderColor(f.theme.BorderFocus).SetTitleAlign(tview.AlignLeft).
		SetTitle(fmt.Sprintf(" %s → %s (%s) ", fk.Name, fk.RefTable.Object, strings.Join(fk.RefColumns, ", ")))
	lk.filter.SetLabel("Filter: ").SetLabelStyle(tcell.StyleDefault.Foreground(f.theme.Accent)).
		SetFieldStyle(tcell.StyleDefault.Foreground(f.theme.Text).Underline(true)).
		SetPlaceholder("type to search all text columns").
		SetPlaceholderStyle(tcell.StyleDefault.Foreground(f.theme.Dim))
	lk.grid = grid.NewWithSource(nil, grid.Options{
		FetchSize: f.opts.FetchSize,
		Fetcher:   func(fn func()) { f.opts.Async(fn, func() {}) },
	})
	f.look = lk
	lk.query()
}

// query (re)runs the lookup query for the current filter text. Queries are
// coalesced: while one runs, a later change only marks pending.
func (lk *lookup) query() {
	if lk.inFlight {
		lk.pending = true
		return
	}
	lk.inFlight = true
	lk.queries++
	lk.status, lk.isErr = "Loading…", false
	f := lk.f
	be := f.opts.Backend
	text := lk.filter.GetText()
	ref := lk.ref
	fk := lk.fk
	limit := f.opts.LookupLimit
	var (
		res *exec.Result
		err error
	)
	f.opts.Async(func() {
		if ref == nil {
			if ref, err = be.Describe(fk.RefTable); err != nil {
				return
			}
		}
		q, args := lookupQuery(be, fk, ref, text, limit)
		res, err = be.Grid(q, args...)
	}, func() {
		lk.inFlight = false
		if lk.closed {
			if res != nil && res.Cursor() != nil {
				f.opts.Async(func() { res.Cursor().Close() }, func() {})
			}
			return
		}
		if ref != nil {
			lk.ref = ref
		}
		if err != nil {
			lk.status, lk.isErr = "Error: "+err.Error(), true
		} else {
			lk.res = res
			lk.grid.SetResult(res)
			lk.status = ""
		}
		if lk.pending {
			lk.pending = false
			lk.query()
		}
	})
}

// lookupQuery builds the lookup SELECT: all columns of the referenced
// table, filtered by the dialect's quick-search predicate, limited.
func lookupQuery(be Backend, fk *meta.ForeignKey, ref *meta.Table, text string, limit int) (string, []any) {
	d := be.Dialect()
	q := "SELECT * FROM " + d.Qualify(fk.RefTable)
	var args []any
	if text != "" && ref != nil {
		pred := d.TextSearchPredicate(ref.Columns, d.Placeholder(1))
		for i := 0; i < d.TextSearchParamCount(ref.Columns); i++ {
			args = append(args, text)
		}
		q += " WHERE " + pred
	}
	return d.LimitQuery(q, limit), args
}

func (lk *lookup) close() {
	lk.closed = true
	if lk.f.look == lk {
		lk.f.look = nil
	}
	if res := lk.res; res != nil && res.Cursor() != nil {
		lk.f.opts.Async(func() { res.Cursor().Close() }, func() {})
	}
}

func (lk *lookup) paste(text string) {
	before := lk.filter.GetText()
	lk.filter.PasteHandler()(text, func(tview.Primitive) {})
	if lk.filter.GetText() != before {
		lk.query()
	}
}

func (lk *lookup) handleKey(ev *tcell.EventKey) bool {
	switch ev.Key() {
	case tcell.KeyEscape:
		lk.close()
		return true
	case tcell.KeyEnter:
		lk.selectCurrent()
		return true
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyLeft, tcell.KeyRight:
		lk.grid.InputHandler()(ev, func(tview.Primitive) {})
		return true
	case tcell.KeyTab, tcell.KeyBacktab:
		return true
	}
	if ev.Key() >= tcell.KeyF1 && ev.Key() <= tcell.KeyF64 {
		return true
	}
	before := lk.filter.GetText()
	lk.filter.InputHandler()(ev, func(tview.Primitive) {})
	if lk.filter.GetText() != before {
		lk.query()
	}
	return true
}

// selectCurrent fills all columns of the foreign key from the selected row.
func (lk *lookup) selectCurrent() {
	row, _ := lk.grid.CurrentCell()
	src := lk.grid.Source()
	if row < 0 || src == nil {
		return
	}
	lk.selectRow(src.Columns(), src.Row(row))
}

func (lk *lookup) selectRow(cols []exec.ColumnInfo, r exec.Row) {
	f, fk := lk.f, lk.fk
	type pair struct {
		fl *field
		v  any
	}
	var set []pair
	for i, c := range fk.Columns {
		if i >= len(fk.RefColumns) {
			break
		}
		idx := resultIndex(cols, fk.RefColumns[i])
		if idx < 0 || idx >= len(r) {
			lk.status, lk.isErr = "column "+fk.RefColumns[i]+" not in the lookup result", true
			return
		}
		if fl := f.fieldOf(c); fl != nil {
			set = append(set, pair{fl, r[idx]})
		}
	}
	desc := ""
	if dc := descColumn(lk.ref, fk.RefColumns); dc != "" {
		if idx := resultIndex(cols, dc); idx >= 0 && idx < len(r) && r[idx] != nil {
			desc = exec.CellText(r[idx])
		}
	}
	for _, p := range set {
		if p.v == nil {
			f.setNull(p.fl)
		} else {
			f.setValue(p.fl, exec.CellText(p.v))
		}
		p.fl.desc = desc
	}
	lk.close()
}

func (lk *lookup) draw(screen tcell.Screen, x, y, w, h int) {
	fill(screen, x, y, w, h, tcell.StyleDefault)
	lk.box.SetBackgroundColor(tcell.ColorDefault)
	lk.box.SetRect(x, y, w, h)
	lk.box.Draw(screen)
	ix, iy, iw, ih := lk.box.GetInnerRect()
	if iw <= 0 || ih <= 0 {
		return
	}
	lk.filter.SetRect(ix, iy, iw, 1)
	lk.filter.Focus(nil)
	lk.filter.Draw(screen)
	if ih > 2 {
		lk.grid.SetRect(ix, iy+1, iw, ih-2)
		lk.grid.Draw(screen)
	}
	st := tcell.StyleDefault.Foreground(lk.f.theme.Dim)
	s := lk.status
	if s == "" {
		s = lk.grid.Status()
		if err := lk.grid.Err(); err != nil {
			s, lk.isErr = "Error: "+err.Error(), true
		}
	}
	if lk.isErr {
		st = tcell.StyleDefault.Foreground(lk.f.theme.Error)
	}
	s += "   Enter select · Esc close"
	put(screen, ix, iy+ih-1, iw, clip(s, iw), st)
}
