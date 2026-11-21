// Package grid is the result grid (spec 10): a tview.Table over virtual
// content backed by a result, with a fixed header row, cell selection,
// horizontal scrolling, no wrapping, lazy loading, sorting, filtering and a
// pivot (transposed) view.
//
// Threading: every method of Grid is called on the UI goroutine. Fetches
// and re-queries run through Options.Fetcher (the application's DB worker);
// results of a re-query are applied through Options.UI.
package grid

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/dialect"
	"dbcc/internal/exec"
)

// Defaults (spec 10.1, 10.2, 10.6, 15).
const (
	DefaultFetchSize        = 500
	DefaultMaxColumnWidth   = 40
	DefaultPivotColumnWidth = 30
	// WidthSampleRows is how many loaded rows are inspected for column widths.
	WidthSampleRows = 200
)

// Fetcher runs fn, which fetches the next chunk of rows or re-queries. The
// default runs fn synchronously. The application passes one that runs fn
// on its DB worker goroutine and then redraws, e.g.
//
//	func(fn func()) { worker.Do(func() { fn(); app.QueueUpdateDraw(func() {}) }) }
type Fetcher func(fn func())

// Options configure a Grid.
type Options struct {
	// FetchSize is the number of rows fetched per chunk (default 500).
	FetchSize int
	// PageSize is the number of rows per page (page_size). Results from
	// the runner bring their page size; a displayed *exec.Result without
	// one gets this (Result.SetPageSize). 0 leaves results unpaged.
	PageSize int
	// MaxColumnWidth caps column widths; longer values end in "…" (default 40).
	MaxColumnWidth int
	// PivotColumnWidth is the width of record columns in the pivot view
	// (pivot_column_width, default 30).
	PivotColumnWidth int
	// Fetcher runs fetches and re-queries (default: synchronous).
	Fetcher Fetcher
	// UI runs f on the UI goroutine (tview: app.QueueUpdateDraw(f)). It is
	// used to apply the outcome of a re-query and of a page change; nil runs f
	// inline on the Fetcher's goroutine, which is only correct with the
	// default synchronous Fetcher.
	UI func(f func())
	// OnSelect is called on Enter with the record (source row index, the
	// index into Result().Rows()), the column and the cell value — in both
	// the normal and the pivot view. The caller opens the cell detail
	// window, typically with CellDetailText(value).
	OnSelect func(row, col int, value any)
	// OnFetched is called after each fetch with the number of rows fetched
	// and the error, on the goroutine the Fetcher runs fn on.
	OnFetched func(n int, err error)

	// Dialect quotes identifiers and builds the quick-search predicate for
	// server-side sorting and filtering of table-origin results.
	Dialect dialect.Dialect
	// Requery runs a generated query for a table-origin result. With
	// Dialect and Requery set, table-origin results are sorted and
	// filtered on the server; otherwise everything is local.
	Requery Requery
	// Placeholder returns the n-th (1-based) bind placeholder for
	// generated queries; nil means the dialect's Placeholder method if it
	// has one, else "?".
	Placeholder func(n int) string
	// OnError receives errors of re-queries (e.g. a rejected WHERE; the
	// previous result stays) and of actions that are not possible (free
	// WHERE on a free result). Called on the UI goroutine.
	OnError func(err error)
	// OnChange is called on the UI goroutine whenever the sort, filter,
	// pivot state or the displayed result changes (refresh the status line).
	OnChange func()

	// OpenPrompt shows p (an input prompt of the grid) as a modal of about
	// width x height cells titled title, and focuses it. The grid closes it
	// through ClosePrompt when the user confirms or cancels (Enter / Esc
	// are handled by p). closed is to be called by the application if it
	// removes the modal by other means; it is safe to call more than once.
	// Without OpenPrompt the search, filter_column and filter_where actions
	// are not available (Action returns false).
	OpenPrompt func(p tview.Primitive, title string, width, height int, closed func())
	// ClosePrompt removes the modal shown by OpenPrompt and returns the
	// focus to the grid.
	ClosePrompt func()
	// CompleteWhere, if set, completes the free WHERE expression
	// (tview.InputField autocomplete: return the candidate full texts).
	CompleteWhere func(text string) []string
}

// Grid is the result grid primitive.
type Grid struct {
	*tview.Table
	opts    Options
	content *content
	height  int // inner height of the last draw (page size)
	width   int // inner width of the last draw

	state State
	gen   int // bumped by SetSource: stale re-queries are dropped
	// requery in flight
	reqBusy atomic.Bool
	hint    string // status hint while something runs ("querying…")
	note    string // status note until the next key ("last page")
	prompt  *promptState
}

// fetchState belongs to one source, so a fetch that finishes after the
// source has been replaced does not touch the new one.
type fetchState struct {
	busy     atomic.Bool
	mu       sync.Mutex
	err      error
	lastN    int
	lastLen  int
	attempts int
}

// New creates a grid over res (which may be nil).
func New(res *exec.Result, opts Options) *Grid {
	var src Source
	if res != nil {
		if opts.PageSize > 0 {
			res.SetPageSize(opts.PageSize)
		}
		src = ResultSource{res}
	}
	return NewWithSource(src, opts)
}

// NewWithSource creates a grid over any Source (which may be nil).
func NewWithSource(src Source, opts Options) *Grid {
	if opts.FetchSize <= 0 {
		opts.FetchSize = DefaultFetchSize
	}
	if opts.MaxColumnWidth <= 0 {
		opts.MaxColumnWidth = DefaultMaxColumnWidth
	}
	if opts.PivotColumnWidth <= 0 {
		opts.PivotColumnWidth = DefaultPivotColumnWidth
	}
	if opts.Fetcher == nil {
		opts.Fetcher = func(fn func()) { fn() }
	}
	g := &Grid{Table: tview.NewTable(), opts: opts}
	g.Table.SetFixed(1, 0).
		SetSelectable(true, true).
		SetSeparator(tview.Borders.Vertical).
		SetBordersColor(tcell.ColorGray)
	g.Table.SetSelectionChangedFunc(func(row, col int) { g.maybeFetch() })
	g.Table.SetSelectedFunc(func(row, col int) {
		if g.opts.OnSelect == nil {
			return
		}
		rec, c, ok := g.cellAt(row, col)
		if !ok {
			return
		}
		g.opts.OnSelect(rec, c, g.content.value(rec, c))
	})
	g.SetSource(src)
	return g
}

// SetResult replaces the displayed result (nil clears the grid). Sort,
// filter and pivot state are reset.
func (g *Grid) SetResult(res *exec.Result) *Grid {
	if res == nil {
		return g.SetSource(nil)
	}
	if g.opts.PageSize > 0 {
		res.SetPageSize(g.opts.PageSize)
	}
	return g.SetSource(ResultSource{res})
}

// SetSource replaces the displayed source (nil clears the grid). Sort,
// filter and pivot state are reset.
func (g *Grid) SetSource(src Source) *Grid {
	g.gen++
	g.state = State{}
	g.hint, g.note = "", ""
	g.content = newContent(src, g.opts.MaxColumnWidth, g.opts.PivotColumnWidth)
	if pk := g.originPK(); pk != nil {
		g.content.setState(State{}, pk)
	}
	g.Table.SetContent(g.content)
	g.Table.SetFixed(1, 0)
	g.Table.SetOffset(0, 0)
	g.Table.Select(1, 0)
	return g
}

// replaceSource shows src with the current state (re-query result),
// keeping the pivot mode and the cursor column / attribute.
func (g *Grid) replaceSource(src Source) {
	_, col := g.cursor()
	pivot := g.content.pivot
	g.content = newContent(src, g.opts.MaxColumnWidth, g.opts.PivotColumnWidth)
	g.content.pivot = pivot
	g.content.setState(g.state, g.originPK())
	g.Table.SetContent(g.content)
	g.Table.SetOffset(0, 0)
	g.selectDisplay(0, col)
}

// Result returns the displayed *exec.Result (after a server-side sort or
// filter: the re-queried one), or nil if the grid shows none or shows
// another kind of Source.
func (g *Grid) Result() *exec.Result {
	if rs, ok := g.content.src.(ResultSource); ok {
		return rs.R
	}
	return nil
}

// Source returns the displayed source (may be nil).
func (g *Grid) Source() Source { return g.content.src }

// Pivot reports whether the pivot view is shown.
func (g *Grid) Pivot() bool { return g.content.pivot }

// cursor returns the selected display row (-1 when there are none) and
// the selected column (attribute), in either view.
func (g *Grid) cursor() (disp, col int) {
	r, c := g.Table.GetSelection()
	if g.content.pivot {
		disp, col = c-1, r-1
	} else {
		disp, col = r-1, c
	}
	if g.content.src == nil || disp < 0 || disp >= g.content.rows() {
		disp = -1
	}
	if col < 0 {
		col = 0
	}
	return disp, col
}

// cellAt maps a table cell to (record, column).
func (g *Grid) cellAt(row, col int) (rec, c int, ok bool) {
	disp := row - 1
	c = col
	if g.content.pivot {
		disp, c = col-1, row-1
	}
	if g.content.src == nil || disp < 0 || disp >= g.content.rows() || c < 0 || c >= len(g.content.cols) {
		return -1, c, false
	}
	return g.content.srcIndex(disp), c, true
}

// selectDisplay selects display row disp (clamped) and column col.
func (g *Grid) selectDisplay(disp, col int) {
	if n := g.content.rows(); disp >= n {
		disp = n - 1
	}
	if disp < 0 {
		disp = 0
	}
	if nc := len(g.content.cols); col >= nc {
		col = nc - 1
	}
	if col < 0 {
		col = 0
	}
	if g.content.pivot {
		g.Table.Select(col+1, disp+1)
	} else {
		g.Table.Select(disp+1, col)
	}
}

// CurrentCell returns the selected record (source row index, i.e. the
// index into Result().Rows(); -1 when there are no rows) and column, in
// both views.
func (g *Grid) CurrentCell() (row, col int) {
	disp, col := g.cursor()
	if disp < 0 {
		return -1, col
	}
	return g.content.srcIndex(disp), col
}

// CurrentRecord returns the selected record as the index into
// Result().Rows() (or Source().Row) and whether there is one. In the pivot
// view this is the record column under the cursor.
func (g *Grid) CurrentRecord() (int, bool) {
	r, _ := g.CurrentCell()
	return r, r >= 0
}

// CurrentColumn returns the selected column (attribute row in the pivot view).
func (g *Grid) CurrentColumn() int {
	_, c := g.cursor()
	return c
}

// CurrentRow returns the selected record's cells.
func (g *Grid) CurrentRow() (exec.Row, bool) {
	r, ok := g.CurrentRecord()
	if !ok {
		return nil, false
	}
	return g.content.src.Row(r), true
}

// CurrentValue returns the value of the selected cell and whether there is one.
func (g *Grid) CurrentValue() (any, bool) {
	r, c := g.CurrentCell()
	if r < 0 || c < 0 || c >= len(g.content.cols) {
		return nil, false
	}
	return g.content.value(r, c), true
}

// Err returns the error of the last fetch, if any.
func (g *Grid) Err() error {
	st := g.content.fetch
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.err
}

// Fetching reports whether a fetch or re-query is in flight.
func (g *Grid) Fetching() bool { return g.content.fetch.busy.Load() || g.reqBusy.Load() }

// ColumnWidth returns the computed display width of column col (normal view).
func (g *Grid) ColumnWidth(col int) int {
	g.content.updateWidths()
	if col < 0 || col >= len(g.content.widths) {
		return 0
	}
	return g.content.widths[col]
}

// Draw draws the grid. Column widths are refreshed while fewer than
// WidthSampleRows rows have been inspected; a local view is brought up to
// date with newly loaded rows; a fetch is started if the selection is near
// the end of the loaded rows. If a visible value further down needs more
// room than the sample gave its column (typically growing ids), the column
// widens (never narrows, never beyond MaxColumnWidth) and the frame is
// drawn again. All of this is O(visible cells) unless a local view has to
// absorb new rows.
func (g *Grid) Draw(screen tcell.Screen) {
	_, _, w, h := g.Table.GetInnerRect()
	g.height, g.width = h, w
	g.syncFollow()
	g.refreshView()
	g.content.updateWidths()
	g.maybeFetch()
	g.Table.Draw(screen)
	if g.content.pivot {
		return
	}
	off, _ := g.Table.GetOffset()
	if g.content.growVisible(off, h-1) {
		g.Table.Draw(screen)
	}
}

// InputHandler is the table's, except for the moves towards the header
// row in the pivot view: tview scans the (non-selectable) header row cell
// by cell, which is O(records) there, so Home, Up and PgUp are done here.
func (g *Grid) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	h := g.Table.InputHandler()
	return func(ev *tcell.EventKey, setFocus func(p tview.Primitive)) {
		g.clearNote()
		if g.emptyKey(ev) {
			return
		}
		if g.content.pivot && g.pivotKey(ev) {
			return
		}
		h(ev, setFocus)
	}
}

// emptyKey handles navigation when there is nothing selectable: a result
// without records has only the non-selectable header row (normal view) or
// attribute column (pivot view). tview's selection search would then loop
// forever, because the selection sits on a cell that never comes round
// again — that froze the UI on empty tables. Left/Right still scroll the
// columns; the other navigation keys do nothing.
func (g *Grid) emptyKey(ev *tcell.EventKey) bool {
	empty := g.content.src == nil || g.content.GetRowCount() <= 1 // header only
	if g.content.pivot {
		empty = g.content.src == nil || g.content.GetColumnCount() <= 1 // attribute column only
	}
	if !empty {
		return false
	}
	rowOff, colOff := g.Table.GetOffset()
	maxCol := max(g.content.GetColumnCount()-1, 0)
	switch ev.Key() {
	case tcell.KeyLeft:
		g.Table.SetOffset(rowOff, max(colOff-1, 0))
		return true
	case tcell.KeyRight:
		g.Table.SetOffset(rowOff, min(colOff+1, maxCol))
		return true
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd,
		tcell.KeyCtrlF, tcell.KeyCtrlB, tcell.KeyTab, tcell.KeyBacktab, tcell.KeyEnter:
		return true
	case tcell.KeyRune:
		switch ev.Rune() {
		case 'h':
			g.Table.SetOffset(rowOff, max(colOff-1, 0))
			return true
		case 'l':
			g.Table.SetOffset(rowOff, min(colOff+1, maxCol))
			return true
		case 'j', 'k', 'g', 'G':
			return true
		}
	}
	return false
}

func (g *Grid) pivotKey(ev *tcell.EventKey) bool {
	row, col := g.Table.GetSelection()
	step := 0
	switch ev.Key() {
	case tcell.KeyHome:
		g.Table.Select(1, 1)
		return true
	case tcell.KeyUp:
		step = 1
	case tcell.KeyPgUp, tcell.KeyCtrlB:
		step = max(g.height-1, 1)
	case tcell.KeyRune:
		switch ev.Rune() {
		case 'g':
			g.Table.Select(1, 1)
			return true
		case 'k':
			step = 1
		default:
			return false
		}
	default:
		return false
	}
	g.Table.Select(max(row-step, 1), max(col, 1))
	return true
}

// syncFollow keeps the cursor of a following source in place when rows
// were appended or dropped from the front since the last draw: on the
// last row it moves to the new last row (like tail -f), otherwise it stays
// on its record.
func (g *Grid) syncFollow() {
	c := g.content
	f, ok := c.src.(Follower)
	if !ok || !f.Follows() {
		return
	}
	n, off := c.src.Len(), 0
	if p, ok := c.src.(Pager); ok {
		off = p.Offset()
	}
	if n == c.seenLen && off == c.seenOff {
		return
	}
	dropped := off - c.seenOff
	disp, col := g.cursor()
	wasLast := c.seenLen == 0 || disp == c.seenLen-1
	c.seenLen, c.seenOff = n, off
	if c.view != nil {
		// Local sort or filter: indices shifted, rebuild the view.
		if dropped > 0 {
			c.view = &localView{rowView: newRowView(c.cols, g.state)}
			c.view.refresh(n, c.rowFunc())
		}
		return
	}
	switch {
	case wasLast:
		g.selectDisplay(n-1, col)
	case dropped > 0:
		g.selectDisplay(max(disp-dropped, 0), col)
		if !c.pivot {
			rowOff, colOff := g.Table.GetOffset()
			g.Table.SetOffset(max(rowOff-dropped, 0), colOff)
		}
	}
}

// refreshView brings a local view up to date with the loaded rows, keeping
// the cursor on the same record.
func (g *Grid) refreshView() {
	c := g.content
	if c.view == nil || c.src == nil {
		return
	}
	n := c.src.Len()
	if n == c.view.n {
		return
	}
	disp, col := g.cursor()
	rec := -1
	if disp >= 0 {
		rec = c.srcIndex(disp)
	}
	sorted := c.view.Sorted()
	c.view.refresh(n, c.rowFunc())
	if sorted && rec >= 0 {
		if p := c.view.position(rec); p >= 0 && p != disp {
			g.selectDisplay(p, col)
		}
	}
}

// pageSize is the number of records visible at once.
func (g *Grid) pageSize() int {
	if g.content.pivot {
		pw := g.opts.PivotColumnWidth + 1
		p := (g.width - g.content.attrWidth() - 1) / pw
		return max(p, 1)
	}
	return max(g.height-1, 1) // minus header
}

// maybeFetch starts a fetch when the selection is within one page of the
// end of the displayed rows and the source can deliver more.
func (g *Grid) maybeFetch() {
	src := g.content.src
	if src == nil {
		return
	}
	st := g.content.fetch
	if st.busy.Load() || g.reqBusy.Load() || !src.More() {
		return
	}
	n := src.Len()
	shown := g.content.rows()
	disp, _ := g.cursor()
	page := g.pageSize()
	if disp < shown-page-1 && shown > 0 {
		return
	}
	st.mu.Lock()
	// A fetch that returned nothing without ending the cursor would loop;
	// do not retry until the row count changes.
	stuck := st.attempts > 0 && st.lastN == 0 && st.lastLen == n
	st.mu.Unlock()
	if stuck {
		return
	}
	if !st.busy.CompareAndSwap(false, true) {
		return
	}
	size := g.opts.FetchSize
	onFetched := g.opts.OnFetched
	g.opts.Fetcher(func() {
		got, err := src.Fetch(size)
		st.mu.Lock()
		st.err, st.lastN, st.lastLen = err, got, n
		st.attempts++
		st.mu.Unlock()
		st.busy.Store(false)
		if onFetched != nil {
			onFetched(got, err)
		}
	})
}

// content is the virtual tview.TableContent over a Source. In the normal
// view row 0 is the header and row i+1 is display row i; in the pivot view
// row 0 is the record header, row j+1 is attribute j, column 0 holds the
// attribute names and column i+1 is display row i. A local view (free
// results with a local sort or filter) maps display rows to source rows.
// All access is O(1).
type content struct {
	tview.TableContentReadOnly
	src        Source
	cols       []exec.ColumnInfo
	maxWidth   int
	widths     []int
	sampled    int // rows inspected for widths so far (-1: never computed)
	fetch      *fetchState
	view       *localView
	sort       []SortKey
	labels     []string // header labels with sort markers
	pivot      bool
	pivotWidth int
	attrW      int   // width of the pivot attribute column
	pk         []int // primary-key columns for pivot record labels
	// Rows and offset of a following source at the last draw (syncFollow).
	seenLen, seenOff int
}

func newContent(src Source, maxWidth, pivotWidth int) *content {
	c := &content{src: src, maxWidth: maxWidth, pivotWidth: pivotWidth, sampled: -1, fetch: &fetchState{}}
	if src != nil {
		c.cols = src.Columns()
		c.seenLen = src.Len()
		if p, ok := src.(Pager); ok {
			c.seenOff = p.Offset()
		}
	}
	c.setState(State{}, nil)
	return c
}

// setState sets the header labels for st (sort markers) and the primary
// key used for pivot record labels; widths are recomputed.
func (c *content) setState(st State, pk []int) {
	c.sort = st.Sort
	c.pk = pk
	c.labels = make([]string, len(c.cols))
	for i, col := range c.cols {
		c.labels[i] = col.Name
		if rank, desc := st.sortRank(i); rank > 0 {
			arrow := " ▲"
			if desc {
				arrow = " ▼"
			}
			c.labels[i] += arrow + strconv.Itoa(rank)
		}
	}
	c.attrW = 1
	for _, l := range c.labels {
		c.attrW = max(c.attrW, textWidth(l, c.maxWidth))
	}
	c.attrW = min(c.attrW, c.maxWidth)
	c.sampled = -1
	c.updateWidths()
}

func (c *content) rowFunc() func(int) exec.Row {
	if rs, ok := c.src.(ResultSource); ok {
		rows := rs.R.Rows()
		return func(i int) exec.Row { return rows[i] }
	}
	return c.src.Row
}

// rows is the number of displayed rows (records).
func (c *content) rows() int {
	if c.src == nil {
		return 0
	}
	if c.view != nil {
		return len(c.view.idx)
	}
	return c.src.Len()
}

// srcIndex maps a display row to a source row.
func (c *content) srcIndex(disp int) int {
	if c.view != nil {
		return c.view.idx[disp]
	}
	return disp
}

// row returns display row disp.
func (c *content) row(disp int) exec.Row {
	return c.src.Row(c.srcIndex(disp))
}

// updateWidths computes column widths from the header and the first
// WidthSampleRows loaded rows; it only rescans while that sample grows.
func (c *content) updateWidths() {
	n := 0
	if c.src != nil {
		n = c.src.Len()
	}
	if n > WidthSampleRows {
		n = WidthSampleRows
	}
	if n == c.sampled {
		return
	}
	c.sampled = n
	c.widths = make([]int, len(c.cols))
	for j := range c.cols {
		w := textWidth(c.labels[j], c.maxWidth)
		for i := 0; i < n && w < c.maxWidth; i++ {
			row := c.src.Row(i)
			if j >= len(row) {
				continue
			}
			s, _ := rawText(row[j])
			if tw := textWidth(s, c.maxWidth); tw > w {
				w = tw
			}
		}
		if w > c.maxWidth {
			w = c.maxWidth
		}
		if w < 1 {
			w = 1
		}
		c.widths[j] = w
	}
}

// growVisible widens columns for the display rows [from, from+n) and
// reports whether any width changed.
func (c *content) growVisible(from, n int) bool {
	if c.src == nil {
		return false
	}
	end := c.rows()
	if from+n < end {
		end = from + n
	}
	grew := false
	for i := max(from, 0); i < end; i++ {
		row := c.row(i)
		for j := range c.widths {
			if j >= len(row) || c.widths[j] >= c.maxWidth {
				continue
			}
			s, _ := rawText(row[j])
			if tw := min(textWidth(s, c.maxWidth), c.maxWidth); tw > c.widths[j] {
				c.widths[j] = tw
				grew = true
			}
		}
	}
	return grew
}

// value returns cell col of source row rec.
func (c *content) value(rec, col int) any {
	if c.src == nil || rec < 0 || rec >= c.src.Len() {
		return nil
	}
	r := c.src.Row(rec)
	if col < 0 || col >= len(r) {
		return nil
	}
	return r[col]
}

func (c *content) GetRowCount() int {
	if c.pivot {
		return len(c.cols) + 1
	}
	return c.rows() + 1
}

func (c *content) GetColumnCount() int {
	if c.pivot {
		return c.rows() + 1
	}
	return len(c.cols)
}

// attrWidth is the width of the attribute-name column of the pivot view.
func (c *content) attrWidth() int { return c.attrW }

// recordLabel is the pivot column header of display row disp: the
// primary-key value(s) for table-origin results with a known key, else the
// record number.
func (c *content) recordLabel(disp int) string {
	if len(c.pk) > 0 {
		r := c.row(disp)
		parts := make([]string, 0, len(c.pk))
		for _, j := range c.pk {
			var v any
			if j < len(r) {
				v = r[j]
			}
			s, _ := rawText(v)
			parts = append(parts, s)
		}
		return strings.Join(parts, ", ")
	}
	off := 0
	if p, ok := c.src.(Pager); ok {
		off = p.Offset()
	}
	return strconv.Itoa(off + c.srcIndex(disp) + 1)
}

// headerCell is a bold, non-selectable cell padded to w so that column
// widths stay stable while scrolling (tview sizes columns from the visible
// rows only).
func headerCell(s string, w int, right bool) *tview.TableCell {
	text, tw := fit(s, w)
	pad := strings.Repeat(" ", max(0, w-tw))
	if right {
		text = pad + text
	} else {
		text += pad
	}
	return tview.NewTableCell(tview.Escape(text)).
		SetAttributes(tcell.AttrBold).
		SetSelectable(false).
		SetMaxWidth(w)
}

func dataCell(v any, w int, right bool) *tview.TableCell {
	s, isNull := rawText(v)
	text, _ := fit(s, w)
	align := tview.AlignLeft
	if right {
		align = tview.AlignRight
	}
	cell := tview.NewTableCell(tview.Escape(text)).SetAlign(align).SetMaxWidth(w)
	if isNull {
		cell.SetAttributes(tcell.AttrDim)
	}
	return cell
}

func (c *content) GetCell(row, col int) *tview.TableCell {
	if c.pivot {
		return c.pivotCell(row, col)
	}
	if col < 0 || col >= len(c.cols) || row < 0 {
		return nil
	}
	w := c.maxWidth
	if col < len(c.widths) {
		w = c.widths[col]
	}
	right := c.cols[col].Numeric
	if row == 0 {
		return headerCell(c.labels[col], w, right)
	}
	if c.src == nil || row-1 >= c.rows() {
		return nil
	}
	r := c.row(row - 1)
	var v any
	if col < len(r) {
		v = r[col]
	}
	return dataCell(v, w, right)
}

func (c *content) pivotCell(row, col int) *tview.TableCell {
	if row < 0 || row > len(c.cols) || col < 0 || col > c.rows() {
		return nil
	}
	aw := c.attrWidth()
	switch {
	case col == 0 && row == 0:
		return headerCell("", aw, false)
	case col == 0:
		return headerCell(c.labels[row-1], aw, false)
	case row == 0:
		return headerCell(c.recordLabel(col-1), c.pivotWidth, false)
	}
	r := c.row(col - 1)
	var v any
	if row-1 < len(r) {
		v = r[row-1]
	}
	return dataCell(v, c.pivotWidth, false)
}
