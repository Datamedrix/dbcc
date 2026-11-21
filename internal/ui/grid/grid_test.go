package grid

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"

	"dbcc/internal/exec"
)

// fakeSource is a Source with a cursor: rows up to total are generated on
// Fetch.
type fakeSource struct {
	mu      sync.Mutex
	cols    []exec.ColumnInfo
	rows    []exec.Row
	total   int
	gen     func(i int) exec.Row
	fetches int
}

func (f *fakeSource) Columns() []exec.ColumnInfo { return f.cols }
func (f *fakeSource) Len() int                   { f.mu.Lock(); defer f.mu.Unlock(); return len(f.rows) }
func (f *fakeSource) Row(i int) exec.Row         { f.mu.Lock(); defer f.mu.Unlock(); return f.rows[i] }
func (f *fakeSource) More() bool                 { f.mu.Lock(); defer f.mu.Unlock(); return len(f.rows) < f.total }
func (f *fakeSource) Status() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := exec.FormatCount(len(f.rows))
	if len(f.rows) < f.total {
		s += "+"
	}
	return s + " rows"
}
func (f *fakeSource) Fetch(n int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	got := 0
	for got < n && len(f.rows) < f.total {
		f.rows = append(f.rows, f.gen(len(f.rows)))
		got++
	}
	return got, nil
}

func newFake(loaded, total int) *fakeSource {
	f := &fakeSource{
		cols:  []exec.ColumnInfo{{Name: "id", Numeric: true}, {Name: "name"}},
		total: total,
		gen:   func(i int) exec.Row { return exec.Row{strconv.Itoa(i + 1), "row" + strconv.Itoa(i+1)} },
	}
	f.Fetch(loaded)
	f.fetches = 0
	return f
}

func newScreen(t testing.TB, w, h int) tcell.SimulationScreen {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	s.SetSize(w, h)
	return s
}

func draw(s tcell.SimulationScreen, p tview.Primitive) {
	w, h := s.Size()
	s.Clear()
	p.SetRect(0, 0, w, h)
	p.Draw(s)
	s.Show()
}

// line returns screen row y as text (one string per cell, wide cells once).
func line(s tcell.Screen, y int) string {
	w, _ := s.Size()
	var b strings.Builder
	for x := 0; x < w; {
		str, _, width := s.Get(x, y)
		if str == "" {
			str = " "
		}
		b.WriteString(str)
		if width < 1 {
			width = 1
		}
		x += width
	}
	return b.String()
}

func screenText(s tcell.Screen) string {
	_, h := s.Size()
	var lines []string
	for y := 0; y < h; y++ {
		lines = append(lines, line(s, y))
	}
	return strings.Join(lines, "\n")
}

func key(g *Grid, k tcell.Key) {
	g.InputHandler()(tcell.NewEventKey(k, 0, tcell.ModNone), func(tview.Primitive) {})
}

func findCell(s tcell.Screen, text string) (x, y int, ok bool) {
	_, h := s.Size()
	for y = 0; y < h; y++ {
		l := line(s, y)
		if i := strings.Index(l, text); i >= 0 {
			return uniseg.StringWidth(l[:i]), y, true
		}
	}
	return 0, 0, false
}

func TestHeaderAndRendering(t *testing.T) {
	long := strings.Repeat("x", 100)
	res := exec.NewResult("t", []exec.ColumnInfo{
		{Name: "id", Numeric: true},
		{Name: "note"},
		{Name: "data", Binary: true},
		{Name: "long"},
	}, []exec.Row{
		{"1", nil, exec.Binary(make([]byte, 42)), long},
		{"12345", "line1\nline2\tx", nil, "short"},
		{"7", "[red]tag", nil, "s"},
	})
	g := New(res, Options{MaxColumnWidth: 20})
	s := newScreen(t, 120, 10)
	draw(s, g)
	txt := screenText(s)

	hdr := line(s, 0)
	for _, h := range []string{"id", "note", "data", "long"} {
		if !strings.Contains(hdr, h) {
			t.Errorf("header %q missing in %q", h, hdr)
		}
	}
	// Header is bold.
	if x, y, _ := findCell(s, "note"); true {
		if _, st, _ := s.Get(x, y); attrs(st)&tcell.AttrBold == 0 || y != 0 {
			t.Errorf("header not bold on row 0 (y=%d)", y)
		}
	}

	// NULL dim.
	x, y, ok := findCell(s, "NULL")
	if !ok {
		t.Fatalf("NULL not drawn:\n%s", txt)
	}
	if _, st, _ := s.Get(x, y); attrs(st)&tcell.AttrDim == 0 {
		t.Errorf("NULL not dim")
	}
	// BLOB.
	if !strings.Contains(txt, "<BLOB 42 bytes>") {
		t.Errorf("blob missing:\n%s", txt)
	}
	// Ellipsis: 100 x's cut to 19 + "…".
	if !strings.Contains(txt, strings.Repeat("x", 19)+"…") || strings.Contains(txt, strings.Repeat("x", 20)) {
		t.Errorf("long value not cut at 20 with ellipsis:\n%s", txt)
	}
	if g.ColumnWidth(3) != 20 {
		t.Errorf("width of long column = %d, want 20", g.ColumnWidth(3))
	}
	// Newline and tab.
	if !strings.Contains(txt, "line1↵line2 x") {
		t.Errorf("newline/tab not rendered:\n%s", txt)
	}
	// Tags are not interpreted.
	if !strings.Contains(txt, "[red]tag") {
		t.Errorf("colour tag interpreted:\n%s", txt)
	}
	// Right alignment of the numeric column (width 5 from "12345").
	if l1, l2 := line(s, 1), line(s, 2); !strings.HasPrefix(l1, "    1│") || !strings.HasPrefix(l2, "12345│") {
		t.Errorf("numeric not right-aligned:\n%s", txt)
	}
	if !strings.HasPrefix(hdr, "   id│") {
		t.Errorf("numeric header not right-aligned: %q", hdr)
	}
	// The selection starts on the first data cell.
	if r, c := g.CurrentCell(); r != 0 || c != 0 {
		t.Errorf("CurrentCell = %d,%d", r, c)
	}
	if g.Result() != res {
		t.Error("Result() mismatch")
	}
	if g.Status() != "3 rows" {
		t.Errorf("Status = %q", g.Status())
	}
}

func TestNoWrap(t *testing.T) {
	v := strings.Repeat("abcdefghij", 50) // 500 chars
	res := exec.NewResult("t", []exec.ColumnInfo{{Name: "v"}, {Name: "w"}}, []exec.Row{{v, "a"}, {"next", "b"}})
	g := New(res, Options{MaxColumnWidth: 1000})
	s := newScreen(t, 80, 6)
	draw(s, g)
	if !strings.HasPrefix(line(s, 1), "abcdefghij") {
		t.Fatalf("row 1: %q", line(s, 1))
	}
	if !strings.HasPrefix(line(s, 2), "next") {
		t.Fatalf("value wrapped; row 2: %q\n%s", line(s, 2), screenText(s))
	}
	if !strings.HasSuffix(strings.TrimRight(line(s, 1), " "), "…") {
		t.Errorf("cut value should end in ellipsis: %q", line(s, 1))
	}
}

func TestHorizontalScroll(t *testing.T) {
	cols := make([]exec.ColumnInfo, 20)
	row := make(exec.Row, 20)
	for i := range cols {
		cols[i] = exec.ColumnInfo{Name: "column_" + strconv.Itoa(i)}
		row[i] = "value_" + strconv.Itoa(i)
	}
	g := New(exec.NewResult("t", cols, []exec.Row{row}), Options{})
	s := newScreen(t, 40, 5)
	draw(s, g)
	if strings.Contains(screenText(s), "column_5") {
		t.Fatal("column_5 should be off screen initially")
	}
	for i := 0; i < 5; i++ {
		key(g, tcell.KeyRight)
	}
	if _, c := g.CurrentCell(); c != 5 {
		t.Fatalf("column = %d, want 5", c)
	}
	draw(s, g)
	txt := screenText(s)
	if !strings.Contains(txt, "column_5") || !strings.Contains(txt, "value_5") {
		t.Fatalf("scrolled view lacks column_5:\n%s", txt)
	}
	if strings.Contains(txt, "column_0") {
		t.Fatalf("column_0 should have scrolled off:\n%s", txt)
	}
	// The selected cell is highlighted.
	x, y, _ := findCell(s, "value_5")
	_, st, _ := s.Get(x, y)
	if fg, bg, _ := st.Decompose(); fg == tcell.ColorDefault && bg == tcell.ColorDefault && attrs(st)&tcell.AttrReverse == 0 {
		t.Error("selected cell not highlighted")
	}
}

func TestLazyFetchNearEnd(t *testing.T) {
	f := newFake(50, 1000)
	g := NewWithSource(f, Options{FetchSize: 100})
	s := newScreen(t, 40, 12)
	draw(s, g)
	if f.fetches != 0 {
		t.Fatalf("fetched %d times before moving", f.fetches)
	}
	if g.Status() != "50+ rows" {
		t.Errorf("Status = %q", g.Status())
	}
	// Move down until within one page (11 data rows) of the end of 50.
	for i := 0; i < 37; i++ {
		key(g, tcell.KeyDown)
	}
	if f.fetches != 0 {
		t.Fatalf("fetched too early at row %v", f.fetches)
	}
	key(g, tcell.KeyDown)
	key(g, tcell.KeyDown)
	if f.fetches != 1 || f.Len() != 150 {
		t.Fatalf("fetches=%d len=%d, want 1/150", f.fetches, f.Len())
	}
	// End jumps to the last loaded row and loads the next chunk.
	key(g, tcell.KeyEnd)
	if r, _ := g.CurrentCell(); r != 149 {
		t.Fatalf("End row = %d", r)
	}
	if f.Len() != 250 {
		t.Fatalf("len after End = %d", f.Len())
	}
	draw(s, g)
	if !strings.Contains(screenText(s), "row150") {
		t.Fatalf("row150 not visible:\n%s", screenText(s))
	}
	// Exhaust: no fetch once More() is false.
	for f.More() {
		key(g, tcell.KeyEnd)
	}
	n := f.fetches
	key(g, tcell.KeyEnd)
	draw(s, g)
	if f.fetches != n {
		t.Fatal("fetched after end")
	}
	if g.Status() != "1,000 rows" {
		t.Errorf("Status = %q", g.Status())
	}
}

func TestAsyncFetcher(t *testing.T) {
	f := newFake(20, 100)
	var pending []func()
	g := NewWithSource(f, Options{FetchSize: 30, Fetcher: func(fn func()) { pending = append(pending, fn) }})
	s := newScreen(t, 40, 12)
	draw(s, g)
	key(g, tcell.KeyEnd)
	if len(pending) != 1 || !g.Fetching() {
		t.Fatalf("pending=%d fetching=%v", len(pending), g.Fetching())
	}
	// No second fetch while one is in flight.
	key(g, tcell.KeyUp)
	key(g, tcell.KeyDown)
	draw(s, g)
	if len(pending) != 1 {
		t.Fatalf("second fetch queued: %d", len(pending))
	}
	done := make(chan struct{})
	go func() { pending[0](); close(done) }() // the "DB worker"
	<-done
	if g.Fetching() || f.Len() != 50 {
		t.Fatalf("fetching=%v len=%d", g.Fetching(), f.Len())
	}
}

func TestEmptyLoadedFetchesFirstChunk(t *testing.T) {
	f := newFake(0, 10)
	g := NewWithSource(f, Options{FetchSize: 5})
	if f.Len() != 5 {
		t.Fatalf("len = %d", f.Len())
	}
	if r, _ := g.CurrentCell(); r != 0 {
		t.Fatalf("row = %d", r)
	}
}

func TestOnSelect(t *testing.T) {
	res := exec.NewResult("t", []exec.ColumnInfo{{Name: "a"}, {Name: "b"}}, []exec.Row{{"x", `{"k":[1,2]}`}})
	var got any
	var gr, gc int
	g := New(res, Options{OnSelect: func(r, c int, v any) { gr, gc, got = r, c, v }})
	key(g, tcell.KeyRight)
	key(g, tcell.KeyEnter)
	if gr != 0 || gc != 1 || got != `{"k":[1,2]}` {
		t.Fatalf("OnSelect(%d,%d,%v)", gr, gc, got)
	}
	want := "{\n  \"k\": [\n    1,\n    2\n  ]\n}"
	if d := CellDetailText(got); d != want {
		t.Fatalf("detail = %q", d)
	}
}

func TestCellDetailText(t *testing.T) {
	if CellDetailText(nil) != "NULL" {
		t.Error("nil")
	}
	if CellDetailText("plain [x] text") != "plain [x] text" {
		t.Error("plain")
	}
	if CellDetailText("{not json") != "{not json" {
		t.Error("invalid json")
	}
	if d := CellDetailText(exec.Binary("AB")); !strings.HasPrefix(d, "<BLOB 2 bytes>") || !strings.Contains(d, "41 42") {
		t.Errorf("binary: %q", d)
	}
}

func TestFit(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 3, "abc"},
		{"abcd", 3, "ab…"},
		{"a\r\nb", 5, "a↵b"},
		{"日本語", 4, "日…"},
		{"日本語", 6, "日本語"},
		{"x\x01y", 5, "x?y"},
		{"abc", 1, "…"},
	}
	for _, c := range cases {
		got, w := fit(c.in, c.max)
		if got != c.want || w != uniseg.StringWidth(got) || w > c.max {
			t.Errorf("fit(%q,%d) = %q,%d want %q", c.in, c.max, got, w, c.want)
		}
	}
}

func TestSetResultResets(t *testing.T) {
	g := New(nil, Options{})
	s := newScreen(t, 20, 5)
	draw(s, g)
	if r, _ := g.CurrentCell(); r != -1 {
		t.Fatalf("empty grid row = %d", r)
	}
	g.SetResult(exec.NewResult("t", []exec.ColumnInfo{{Name: "a"}}, []exec.Row{{"1"}, {"2"}}))
	key(g, tcell.KeyDown)
	if r, _ := g.CurrentCell(); r != 1 {
		t.Fatalf("row = %d", r)
	}
	g.SetResult(exec.NewResult("t", []exec.ColumnInfo{{Name: "b"}}, []exec.Row{{"9"}}))
	if r, _ := g.CurrentCell(); r != 0 {
		t.Fatalf("row after SetResult = %d", r)
	}
	draw(s, g)
	if !strings.HasPrefix(line(s, 1), "9") {
		t.Fatalf("row 1 = %q", line(s, 1))
	}
}

func attrs(st tcell.Style) tcell.AttrMask { _, _, a := st.Decompose(); return a }
