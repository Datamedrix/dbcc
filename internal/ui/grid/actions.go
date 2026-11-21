package grid

import (
	"errors"
	"strconv"
	"strings"

	"dbcc/internal/exec"
)

// ErrWhereFreeResult is reported when a free WHERE is requested on a
// result that is not table-origin (spec 10.5).
var ErrWhereFreeResult = errors.New("free WHERE is only available for table-origin results")

// ErrBusy is reported when a sort or filter is requested while a re-query
// is still running.
var ErrBusy = errors.New("a re-query is still running")

// Action runs a grid action by its keymap name (spec 14, keys.grid) and
// reports whether the grid handled it:
//
//	pivot          toggle the pivot view
//	sort           cycle the current column asc → desc → off
//	sort_add       add the current column as a further sort key (or cycle it)
//	search         quick-search prompt              (needs Options.OpenPrompt)
//	filter_column  column-filter prompt              (needs Options.OpenPrompt)
//	filter_where   free WHERE prompt, table-origin   (needs Options.OpenPrompt)
//	clear_filters  remove the only active filter/sort, or a menu to pick one
//	next_page      drop the page and show the next one (spec 10.2)
//	prev_page      show the previous page (re-runs a read statement)
//	rerun          re-query a table-origin result with an active sort/filter
//
// Other names (detail, edit_row, new_row, copy_*, rerun of free results …)
// return false and stay with the application, which uses CurrentRecord,
// CurrentColumn and CurrentRow to find the record in either view.
func (g *Grid) Action(name string) bool {
	g.clearNote()
	switch name {
	case "pivot":
		g.TogglePivot()
	case "sort", "sort_add":
		if len(g.content.cols) > 0 {
			g.SetState(g.state.cycleSort(g.CurrentColumn(), name == "sort_add"))
		}
	case "search":
		return g.openSearch()
	case "filter_column":
		return g.openColumnFilter()
	case "filter_where":
		if !g.TableOrigin() {
			g.fail(ErrWhereFreeResult)
			return true
		}
		return g.openWhere()
	case "clear_filters":
		items := g.ActiveItems()
		switch {
		case len(items) == 0:
		case len(items) == 1:
			g.Remove(0)
		case !g.openRemoveMenu(items):
			g.RemoveAll()
		}
	case "next_page":
		g.NextPage()
	case "prev_page":
		g.PrevPage()
	case "rerun":
		b := g.builder()
		if b == nil || !g.state.Active() {
			return false
		}
		g.requery(b, g.state)
	default:
		return false
	}
	return true
}

// State returns a copy of the sort and filter state.
func (g *Grid) State() State { return g.state.clone() }

// TableOrigin reports whether sorting and filtering run on the server: the
// result is table-origin and Options.Dialect and Options.Requery are set.
func (g *Grid) TableOrigin() bool { return g.builder() != nil }

func (g *Grid) builder() *QueryBuilder {
	if g.opts.Requery == nil || g.opts.Dialect == nil {
		return nil
	}
	b := NewQueryBuilder(g.opts.Dialect, g.Result())
	if b != nil {
		b.Placeholder = g.opts.Placeholder
	}
	return b
}

// Query returns the server-side query and arguments the current state
// generates, or ok=false for results that are not sorted/filtered on the
// server.
func (g *Grid) Query() (sql string, args []any, ok bool) {
	b := g.builder()
	if b == nil {
		return "", nil, false
	}
	sql, args = b.Build(g.state)
	return sql, args, true
}

// originPK maps the primary key of a table-origin result to column indices.
func (g *Grid) originPK() []int {
	res := g.Result()
	if res == nil || res.Origin == nil || res.Origin.Table == nil || res.Origin.Table.PrimaryKey == nil {
		return nil
	}
	var out []int
	for _, name := range res.Origin.Table.PrimaryKey.Columns {
		found := -1
		for i, c := range res.Columns {
			if strings.EqualFold(c.Name, name) {
				found = i
				break
			}
		}
		if found < 0 {
			return nil
		}
		out = append(out, found)
	}
	return out
}

func (g *Grid) fail(err error) {
	if err != nil && g.opts.OnError != nil {
		g.opts.OnError(err)
	}
}

func (g *Grid) changed() {
	if g.opts.OnChange != nil {
		g.opts.OnChange()
	}
}

func (g *Grid) runUI(f func()) {
	if g.opts.UI != nil {
		g.opts.UI(f)
	} else {
		f()
	}
}

// SetState applies a sort and filter state. Table-origin results are
// re-queried (asynchronously through Fetcher; on error the previous
// result and state stay and the error goes to OnError). Free results are
// sorted and filtered locally on the loaded rows; a free WHERE is rejected
// with ErrWhereFreeResult. The cursor stays in its column and moves to the
// first record.
func (g *Grid) SetState(st State) {
	if g.content.src == nil {
		return
	}
	st = st.clone()
	if b := g.builder(); b != nil {
		g.requery(b, st)
		return
	}
	if st.Where != "" {
		g.fail(ErrWhereFreeResult)
		st.Where = ""
	}
	g.state = st
	g.applyLocal()
	g.changed()
}

// applyLocal rebuilds the local view for g.state.
func (g *Grid) applyLocal() {
	c := g.content
	_, col := g.cursor()
	c.setState(g.state, g.originPK())
	if len(g.state.Sort) > 0 || g.state.Search != "" || len(g.state.Filters) > 0 {
		c.view = &localView{rowView: newRowView(c.cols, g.state)}
		c.view.refresh(c.src.Len(), c.rowFunc())
	} else {
		c.view = nil
	}
	g.Table.SetOffset(0, 0)
	g.selectDisplay(0, col)
}

func (g *Grid) requery(b *QueryBuilder, st State) {
	if !g.reqBusy.CompareAndSwap(false, true) {
		g.fail(ErrBusy)
		return
	}
	q, args := b.Build(st)
	gen := g.gen
	origin := g.Result().Origin
	g.hint = "querying…"
	g.changed()
	rq := g.opts.Requery
	g.opts.Fetcher(func() {
		res, err := rq(q, args)
		g.runUI(func() { g.finishRequery(gen, st, origin, res, err) })
	})
}

func (g *Grid) finishRequery(gen int, st State, origin *exec.Origin, res *exec.Result, err error) {
	g.reqBusy.Store(false)
	g.hint = ""
	if gen != g.gen {
		return // the grid shows another result meanwhile
	}
	if err == nil && res == nil {
		err = errors.New("re-query returned no result")
	}
	if err != nil {
		g.fail(err)
		g.changed()
		return
	}
	if res.Origin == nil {
		res.Origin = origin
	}
	// The re-queried result replaces a following one, which stops.
	if f, ok := g.content.src.(Follower); ok && f.Follows() {
		go f.StopFollow()
	}
	g.state = st
	g.replaceSource(ResultSource{res})
	g.changed()
}

// ErrNoPaging is reported when the displayed source has no pages.
var ErrNoPaging = errors.New("this result has no pages")

// NextPage drops the current page and shows the next one (through
// Fetcher). On the last page the status shows "last page".
func (g *Grid) NextPage() { g.movePage(true) }

// PrevPage shows the previous page (through Fetcher): the statement is
// re-run from the start and the rows before the page are skipped (status
// hint "skipping N rows…"; cancellable with the session cancel).
func (g *Grid) PrevPage() { g.movePage(false) }

// Page returns the 1-based page number and whether the source pages.
func (g *Grid) Page() (int, bool) {
	if p, ok := g.content.src.(Pager); ok {
		return p.Page(), true
	}
	return 1, false
}

func (g *Grid) movePage(next bool) {
	if g.content.src == nil {
		return
	}
	if f, ok := g.content.src.(Follower); ok && f.Follows() {
		g.setNote(exec.ErrFollowing.Error())
		return
	}
	p, ok := g.content.src.(Pager)
	if !ok {
		g.fail(ErrNoPaging)
		return
	}
	st := g.content.fetch
	if g.reqBusy.Load() || st.busy.Load() {
		g.fail(ErrBusy)
		return
	}
	switch {
	case next && !p.HasNextPage():
		g.setNote("last page")
		return
	case !next && p.Page() <= 1:
		g.setNote("first page")
		return
	}
	if !st.busy.CompareAndSwap(false, true) {
		g.fail(ErrBusy)
		return
	}
	if n := p.SkipRows(next); n > 0 {
		g.hint = "skipping " + exec.FormatCount(n) + " rows…"
	} else {
		g.hint = "loading page…"
	}
	g.changed()
	gen := g.gen
	g.opts.Fetcher(func() {
		var err error
		if next {
			err = p.NextPage()
		} else {
			err = p.PrevPage()
		}
		st.busy.Store(false)
		g.runUI(func() { g.finishPage(gen, err) })
	})
}

func (g *Grid) finishPage(gen int, err error) {
	if gen != g.gen {
		return // the grid shows another result meanwhile
	}
	g.hint = ""
	switch {
	case errors.Is(err, exec.ErrLastPage):
		g.note = "last page"
	case errors.Is(err, exec.ErrFirstPage):
		g.note = "first page"
	case err != nil:
		g.fail(err)
	}
	g.showPage()
	g.changed()
}

// showPage rebuilds the content for the page the source shows now: widths,
// local sort and filter are recomputed for the page; pivot mode and the
// cursor column stay, the cursor goes to the first record.
func (g *Grid) showPage() {
	_, col := g.cursor()
	pivot := g.content.pivot
	g.content = newContent(g.content.src, g.opts.MaxColumnWidth, g.opts.PivotColumnWidth)
	g.content.pivot = pivot
	g.Table.SetContent(g.content)
	if g.builder() != nil {
		g.content.setState(g.state, g.originPK())
		g.Table.SetOffset(0, 0)
		g.selectDisplay(0, col)
		return
	}
	g.applyLocal()
}

func (g *Grid) setNote(s string) {
	g.note = s
	g.changed()
}

func (g *Grid) clearNote() {
	if g.note != "" {
		g.note = ""
		g.changed()
	}
}

// TogglePivot switches between the normal and the pivot view; the cursor
// keeps pointing at the same record and attribute.
func (g *Grid) TogglePivot() {
	disp, col := g.cursor()
	if disp < 0 {
		disp = 0
	}
	c := g.content
	c.pivot = !c.pivot
	if c.pivot {
		g.Table.SetFixed(1, 1)
	} else {
		g.Table.SetFixed(1, 0)
	}
	g.Table.SetOffset(0, 0)
	g.selectDisplay(disp, col)
	g.changed()
}

// ActiveItems lists the active sort and filters for the "x" menu, in the
// order Remove takes: the sort (all keys, one item), the quick search,
// each column filter, the free WHERE.
func (g *Grid) ActiveItems() []string {
	var out []string
	if s := g.sortText(); s != "" {
		out = append(out, "sort: "+s)
	}
	if g.state.Search != "" {
		out = append(out, "search: "+strconv.Quote(g.state.Search))
	}
	for _, f := range g.state.Filters {
		out = append(out, "filter: "+filterText(g.colName(f.Col), f))
	}
	if g.state.Where != "" {
		out = append(out, "where: ("+g.state.Where+")")
	}
	return out
}

// Remove removes item i of ActiveItems.
func (g *Grid) Remove(i int) {
	st := g.state.clone()
	k := 0
	if len(st.Sort) > 0 {
		if i == k {
			st.Sort = nil
			g.SetState(st)
			return
		}
		k++
	}
	if st.Search != "" {
		if i == k {
			st.Search = ""
			g.SetState(st)
			return
		}
		k++
	}
	if j := i - k; j >= 0 && j < len(st.Filters) {
		st.Filters = append(st.Filters[:j:j], st.Filters[j+1:]...)
		g.SetState(st)
		return
	}
	k += len(st.Filters)
	if st.Where != "" && i == k {
		st.Where = ""
		g.SetState(st)
	}
}

// RemoveAll removes all filters and the sort.
func (g *Grid) RemoveAll() {
	if g.state.Active() {
		g.SetState(State{})
	}
}

// SetSearch sets the quick search ("" removes it).
func (g *Grid) SetSearch(text string) {
	st := g.state.clone()
	st.Search = text
	g.SetState(st)
}

// AddFilter adds a column filter.
func (g *Grid) AddFilter(f ColumnFilter) {
	if f.Col < 0 || f.Col >= len(g.content.cols) {
		return
	}
	st := g.state.clone()
	st.Filters = append(st.Filters, f)
	g.SetState(st)
}

// SetWhere sets the free WHERE expression ("" removes it).
func (g *Grid) SetWhere(expr string) {
	st := g.state.clone()
	st.Where = strings.TrimSpace(expr)
	g.SetState(st)
}

// View returns the local sort and filter for export (spec 10.7), or nil
// when none is active (no local view, or the result is sorted and
// filtered on the server). The returned view is immutable.
func (g *Grid) View() exec.View {
	if g.content.view == nil {
		return nil
	}
	return g.content.view.rowView
}

func (g *Grid) colName(i int) string {
	if i >= 0 && i < len(g.content.cols) {
		return g.content.cols[i].Name
	}
	return "?"
}

func (g *Grid) sortText() string {
	var parts []string
	for _, k := range g.state.Sort {
		arrow := " ▲"
		if k.Desc {
			arrow = " ▼"
		}
		parts = append(parts, g.colName(k.Col)+arrow)
	}
	return strings.Join(parts, ", ")
}

// StatusParts are the grid's parts of the status line.
type StatusParts struct {
	Rows   string // "1,204+ rows", "rows 3,001–6,000 · page 2 +", "… · partial"
	Sort   string // "name ▲, city ▼" ("" = none)
	Filter string // `search "foo" AND city starts with "B" AND (id > 3)` ("" = none)
	// Hint is "sorted within N loaded rows", "filtered within N loaded
	// rows (M match)" (within the current page), "querying…",
	// "skipping N rows…", "loading page…", "last page", "first page" or "".
	Hint  string
	Pivot bool
}

// StatusParts returns the status-line parts.
func (g *Grid) StatusParts() StatusParts {
	var p StatusParts
	if g.content.src == nil {
		return p
	}
	p.Rows = g.content.src.Status()
	p.Sort = g.sortText()
	var f []string
	if g.state.Search != "" {
		f = append(f, "search "+strconv.Quote(g.state.Search))
	}
	for _, cf := range g.state.Filters {
		f = append(f, filterText(g.colName(cf.Col), cf))
	}
	if g.state.Where != "" {
		f = append(f, "("+g.state.Where+")")
	}
	p.Filter = strings.Join(f, " AND ")
	p.Pivot = g.content.pivot
	switch {
	case g.hint != "":
		p.Hint = g.hint
	case g.note != "":
		p.Hint = g.note
	case g.content.view != nil:
		v := g.content.view
		n := exec.FormatCount(g.content.src.Len())
		switch {
		case v.Sorted() && v.filtered():
			p.Hint = "sorted and filtered within " + n + " loaded rows (" + exec.FormatCount(len(v.idx)) + " match)"
		case v.Sorted():
			p.Hint = "sorted within " + n + " loaded rows"
		default:
			p.Hint = "filtered within " + n + " loaded rows (" + exec.FormatCount(len(v.idx)) + " match)"
		}
	}
	return p
}

// Status is the grid's status-line text: the row count, then
// "sort: …", "filter: …" and the hint when present, separated by two
// spaces, e.g. `1,204+ rows  sort: name ▲  sorted within 1,204 loaded rows`.
func (g *Grid) Status() string {
	g.refreshView()
	p := g.StatusParts()
	parts := []string{p.Rows}
	if p.Sort != "" {
		parts = append(parts, "sort: "+p.Sort)
	}
	if p.Filter != "" {
		parts = append(parts, "filter: "+p.Filter)
	}
	if p.Hint != "" {
		parts = append(parts, p.Hint)
	}
	return strings.Join(parts, "  ")
}
