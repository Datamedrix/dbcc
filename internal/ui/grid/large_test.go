package grid

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/exec"
)

const millionRows = 1_000_000

var (
	millionOnce sync.Once
	million     *exec.Result
)

// millionResult builds a fully buffered 1e6-row result once per test binary.
func millionResult() *exec.Result {
	millionOnce.Do(func() {
		cols := []exec.ColumnInfo{
			{Name: "id", Numeric: true}, {Name: "name"}, {Name: "amount", Numeric: true},
			{Name: "note"}, {Name: "blob", Binary: true},
		}
		names := []string{"alpha", "beta", "gamma", "delta", strings.Repeat("long text ", 10)}
		blob := exec.Binary(make([]byte, 16))
		rows := make([]exec.Row, millionRows)
		cells := make([]any, millionRows*len(cols))
		for i := range rows {
			r := exec.Row(cells[i*len(cols) : (i+1)*len(cols) : (i+1)*len(cols)])
			r[0] = strconv.Itoa(i + 1)
			r[1] = names[i%len(names)]
			r[2] = "12.50"
			if i%7 != 0 {
				r[3] = "note"
			}
			r[4] = blob
			rows[i] = r
		}
		million = exec.NewResult("big", cols, rows)
	})
	return million
}

// TestMillionRowsResponsive proves that drawing a frame and PgDn/End/Home
// navigation on a 1,000,000-row result do not depend on the row count.
// Thresholds are generous (CI, -race); the benchmarks give real numbers.
func TestMillionRowsResponsive(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	res := millionResult()
	g := New(res, Options{})
	s := newScreen(t, 200, 50)

	// timed fails if f, which draws `frames` frames, averages more than
	// 100 ms per frame.
	timed := func(name string, frames int, f func()) {
		t.Helper()
		start := time.Now()
		f()
		d := time.Since(start) / time.Duration(frames)
		if d > 100*time.Millisecond && !raceEnabled {
			t.Errorf("%s: %v per frame", name, d)
		} else {
			t.Logf("%s: %v per frame", name, d)
		}
	}
	timed("first frame", 1, func() { draw(s, g) })
	timed("100 x PgDn + frame", 1, func() {
		for i := 0; i < 100; i++ {
			key(g, tcell.KeyPgDn)
		}
		draw(s, g)
	})
	if r, _ := g.CurrentCell(); r < 100*40 {
		t.Fatalf("PgDn moved to row %d only", r)
	}
	timed("End + frame", 1, func() { key(g, tcell.KeyEnd); draw(s, g) })
	if r, _ := g.CurrentCell(); r != millionRows-1 {
		t.Fatalf("End row = %d", r)
	}
	if !strings.Contains(screenText(s), "1000000") {
		t.Fatalf("last row not visible:\n%s", screenText(s))
	}
	timed("Home + frame", 1, func() { key(g, tcell.KeyHome); draw(s, g) })
	if r, _ := g.CurrentCell(); r != 0 {
		t.Fatalf("Home row = %d", r)
	}
	timed("50 x (Right, frame, Left, frame)", 100, func() {
		for i := 0; i < 50; i++ {
			key(g, tcell.KeyRight)
			draw(s, g)
			key(g, tcell.KeyLeft)
			draw(s, g)
		}
	})
}

func BenchmarkDrawMillion(b *testing.B) {
	g := New(millionResult(), Options{})
	s := newScreen(b, 200, 50)
	draw(s, g)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		draw(s, g)
	}
}

func BenchmarkPgDnMillion(b *testing.B) {
	g := New(millionResult(), Options{})
	s := newScreen(b, 200, 50)
	draw(s, g)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key(g, tcell.KeyPgDn)
		if r, _ := g.CurrentCell(); r >= millionRows-1 {
			key(g, tcell.KeyHome)
		}
		draw(s, g)
	}
}

func BenchmarkEndHomeMillion(b *testing.B) {
	g := New(millionResult(), Options{})
	s := newScreen(b, 200, 50)
	draw(s, g)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key(g, tcell.KeyEnd)
		draw(s, g)
		key(g, tcell.KeyHome)
		draw(s, g)
	}
}

// BenchmarkLazyFetchMillion scrolls through a cursor-backed source of 1e6
// rows with PgDn, fetching 500 rows at a time.
func BenchmarkLazyFetchMillion(b *testing.B) {
	for i := 0; i < b.N; i++ {
		f := newFake(500, millionRows)
		g := NewWithSource(f, Options{})
		s := newScreen(b, 200, 50)
		draw(s, g)
		for f.More() {
			key(g, tcell.KeyEnd)
		}
		draw(s, g)
		if f.Len() != millionRows {
			b.Fatal(f.Len())
		}
	}
}

// TestMillionRowsPivot proves that the pivot view (one column per record)
// stays O(visible cells) on 1,000,000 records.
func TestMillionRowsPivot(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	g := New(millionResult(), Options{})
	s := newScreen(t, 200, 50)
	draw(s, g)
	start := time.Now()
	g.Action("pivot")
	draw(s, g)
	key(g, tcell.KeyEnd)
	draw(s, g)
	if r, _ := g.CurrentCell(); r != millionRows-1 {
		t.Fatalf("End record = %d", r)
	}
	for i := 0; i < 20; i++ {
		key(g, tcell.KeyLeft)
		draw(s, g)
	}
	key(g, tcell.KeyHome)
	draw(s, g)
	g.Action("pivot")
	draw(s, g)
	if d := time.Since(start) / 25; d > 100*time.Millisecond && !raceEnabled {
		t.Errorf("pivot: %v per frame", d)
	} else {
		t.Logf("pivot: %v per frame", d)
	}
}
