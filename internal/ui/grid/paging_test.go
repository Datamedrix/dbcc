package grid

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/exec"
)

// pagedFake is a Source and Pager over total generated rows, pageSize per
// page; rows of the page load lazily. Moving back counts as a re-run.
type pagedFake struct {
	mu                   sync.Mutex
	cols                 []exec.ColumnInfo
	total, ps, off       int
	rows                 []exec.Row
	reruns, nexts, prevs int
}

func newPagedFake(total, ps, loaded int) *pagedFake {
	f := &pagedFake{cols: []exec.ColumnInfo{{Name: "id", Numeric: true}, {Name: "name"}}, total: total, ps: ps}
	f.Fetch(loaded)
	return f
}

func (f *pagedFake) Columns() []exec.ColumnInfo { return f.cols }
func (f *pagedFake) Len() int                   { f.mu.Lock(); defer f.mu.Unlock(); return len(f.rows) }
func (f *pagedFake) Row(i int) exec.Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.rows) {
		return nil
	}
	return f.rows[i]
}
func (f *pagedFake) pageEnd() int { return min(f.off+f.ps, f.total) }
func (f *pagedFake) More() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.off+len(f.rows) < f.pageEnd()
}
func (f *pagedFake) Fetch(n int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	got := 0
	for got < n && f.off+len(f.rows) < f.pageEnd() {
		i := f.off + len(f.rows) + 1
		f.rows = append(f.rows, exec.Row{strconv.Itoa(i), "row" + strconv.Itoa(i)})
		got++
	}
	return got, nil
}
func (f *pagedFake) Status() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := "rows " + exec.FormatCount(f.off+1) + "–" + exec.FormatCount(f.off+len(f.rows)) + " · page " + strconv.Itoa(f.off/f.ps+1)
	if f.off+f.ps < f.total {
		s += " +"
	}
	return s
}
func (f *pagedFake) Page() int         { f.mu.Lock(); defer f.mu.Unlock(); return f.off/f.ps + 1 }
func (f *pagedFake) Offset() int       { f.mu.Lock(); defer f.mu.Unlock(); return f.off }
func (f *pagedFake) HasNextPage() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.off+f.ps < f.total }
func (f *pagedFake) SkipRows(next bool) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if next {
		return 0
	}
	return max(f.off-f.ps, 0)
}
func (f *pagedFake) NextPage() error {
	f.mu.Lock()
	if f.off+f.ps >= f.total {
		f.mu.Unlock()
		return exec.ErrLastPage
	}
	f.nexts++
	f.off += f.ps
	f.rows = nil
	f.mu.Unlock()
	f.Fetch(5)
	return nil
}
func (f *pagedFake) PrevPage() error {
	f.mu.Lock()
	if f.off == 0 {
		f.mu.Unlock()
		return exec.ErrFirstPage
	}
	f.prevs++
	f.reruns++
	f.off -= f.ps
	f.rows = nil
	f.mu.Unlock()
	f.Fetch(5)
	return nil
}

var _ Pager = (*pagedFake)(nil)

func TestPagingActionsAndStatus(t *testing.T) {
	f := newPagedFake(25, 10, 5)
	g := NewWithSource(f, Options{FetchSize: 5})
	s := newScreen(t, 40, 20)
	draw(s, g)
	draw(s, g) // the page is small: Draw loads the rest of it
	if f.Len() != 10 || f.More() {
		t.Fatalf("page 1 not loaded up to the page size: %d", f.Len())
	}
	if st := g.Status(); st != "rows 1–10 · page 1 +" {
		t.Fatalf("status %q", st)
	}
	if !g.Action("next_page") {
		t.Fatal("next_page not handled")
	}
	if p, ok := g.Page(); !ok || p != 2 || f.Offset() != 10 {
		t.Fatalf("page %d %v offset %d", p, ok, f.Offset())
	}
	if g.Status() != "rows 11–20 · page 2 +" { // the rest of the small page loads at once
		t.Fatalf("status %q", g.Status())
	}
	// Page-relative records: the first record of page 2 is row id 11.
	if r, ok := g.CurrentRecord(); !ok || r != 0 {
		t.Fatalf("record %d %v", r, ok)
	}
	key(g, tcell.KeyDown)
	key(g, tcell.KeyDown)
	if r, _ := g.CurrentRecord(); r != 2 {
		t.Fatalf("record after two downs = %d", r)
	}
	if row, ok := g.CurrentRow(); !ok || row[0] != "13" {
		t.Fatalf("current row %v", row)
	}
	if v, _ := g.CurrentValue(); v != "13" {
		t.Fatalf("current value %v", v)
	}
	draw(s, g)
	if !strings.Contains(screenText(s), "row11") || strings.Contains(screenText(s), "row10 ") {
		t.Fatalf("page 2 not shown:\n%s", screenText(s))
	}
	g.Action("next_page")
	draw(s, g)
	if p, _ := g.Page(); p != 3 || f.Len() != 5 || f.More() {
		t.Fatalf("page 3: %d rows, more %v", f.Len(), f.More())
	}
	g.Action("next_page")
	if p, _ := g.Page(); p != 3 || !strings.Contains(g.Status(), "last page") {
		t.Fatalf("on the last page: page %d status %q", p, g.Status())
	}
	if f.nexts != 2 {
		t.Fatalf("NextPage called %d times", f.nexts)
	}
	key(g, tcell.KeyUp) // any key clears the note
	if strings.Contains(g.Status(), "last page") {
		t.Fatalf("note not cleared: %q", g.Status())
	}
	g.Action("prev_page")
	g.Action("prev_page")
	if p, _ := g.Page(); p != 1 || f.reruns != 2 {
		t.Fatalf("back to page 1: page %d reruns %d", p, f.reruns)
	}
	g.Action("prev_page")
	if !strings.Contains(g.Status(), "first page") || f.prevs != 2 {
		t.Fatalf("status %q prevs %d", g.Status(), f.prevs)
	}
}

func TestPagingHintWhileSkippingAndBusy(t *testing.T) {
	f := newPagedFake(100, 10, 10)
	var queued []func()
	var uiQueued []func()
	g := NewWithSource(f, Options{
		FetchSize: 10,
		Fetcher:   func(fn func()) { queued = append(queued, fn) },
		UI:        func(fn func()) { uiQueued = append(uiQueued, fn) },
	})
	run := func() {
		for len(queued) > 0 || len(uiQueued) > 0 {
			q := queued
			queued = nil
			for _, fn := range q {
				fn()
			}
			u := uiQueued
			uiQueued = nil
			for _, fn := range u {
				fn()
			}
		}
	}
	for i := 0; i < 3; i++ {
		g.Action("next_page")
		run()
	}
	if p, _ := g.Page(); p != 4 {
		t.Fatalf("page %d", p)
	}
	var errs []error
	g.opts.OnError = func(err error) { errs = append(errs, err) }
	g.Action("prev_page")
	if !strings.Contains(g.Status(), "skipping 20 rows…") {
		t.Fatalf("hint while re-running: %q", g.Status())
	}
	if !g.Fetching() {
		t.Fatal("page load not reported as fetching")
	}
	g.Action("next_page") // busy: refused, no second job
	if len(queued) != 1 || len(errs) != 1 || errs[0] != ErrBusy {
		t.Fatalf("queued %d errs %v", len(queued), errs)
	}
	run()
	if p, _ := g.Page(); p != 3 || strings.Contains(g.Status(), "skipping") {
		t.Fatalf("page %d status %q", p, g.Status())
	}
	// A page result that arrives after the grid shows another source is dropped.
	g.Action("next_page")
	g.SetSource(newFake(3, 3))
	run()
	if g.Source() == Source(f) || g.content.rows() != 3 {
		t.Fatal("stale page change applied to the new source")
	}
}

func TestPagingPivotAndLocalSort(t *testing.T) {
	f := newPagedFake(25, 10, 10)
	g := NewWithSource(f, Options{FetchSize: 5, PivotColumnWidth: 8})
	s := newScreen(t, 80, 6)
	g.Action("pivot")
	g.Action("next_page")
	if !g.Pivot() {
		t.Fatal("pivot lost on page change")
	}
	draw(s, g)
	if got := g.content.recordLabel(0); got != "11" {
		t.Fatalf("pivot record label %q, want the absolute row number 11", got)
	}
	key(g, tcell.KeyRight) // next record column
	if r, _ := g.CurrentRecord(); r != 1 {
		t.Fatalf("pivot record = %d", r)
	}
	if row, _ := g.CurrentRow(); row[0] != "12" {
		t.Fatalf("pivot current row %v", row)
	}
	g.Action("pivot")
	g.Action("sort")
	g.Action("sort") // id desc within the page
	f.Fetch(10)      // load the rest of page 2
	if st := g.Status(); !strings.Contains(st, "sorted within 10 loaded rows") {
		t.Fatalf("status %q", st)
	}
	if got := g.content.row(0)[0]; got != "20" {
		t.Fatalf("sorted page 2 starts with %v", got)
	}
	g.Action("next_page")
	f.Fetch(10)
	g.Status()
	if got := g.content.row(0)[0]; got != "25" || g.State().Sort == nil {
		t.Fatalf("sort not re-applied to page 3: first %v state %+v", got, g.State())
	}
	if row, _ := g.CurrentRow(); row[0] != "25" {
		t.Fatalf("current row with local sort %v", row)
	}
}

// TestPagingResultSourceUnpaged: a buffered result has one page.
func TestPagingResultSourceUnpaged(t *testing.T) {
	res := exec.NewResult("r", []exec.ColumnInfo{{Name: "a"}}, []exec.Row{{"1"}, {"2"}})
	g := New(res, Options{})
	g.Action("next_page")
	if !strings.Contains(g.Status(), "last page") || g.Status() != "2 rows  last page" {
		t.Fatalf("status %q", g.Status())
	}
	g.Action("prev_page")
	if !strings.Contains(g.Status(), "first page") {
		t.Fatalf("status %q", g.Status())
	}
}
