package grid

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/exec"
)

// TestEmptyResultKeysDoNotHang reproduces the freeze reported on empty
// tables (MSSQL helios.event_mgmt.sites, 0 rows): navigation keys on a
// result without records sent tview's selection search into an endless loop.
func TestEmptyResultKeysDoNotHang(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id", Numeric: true}, {Name: "name"}, {Name: "city"}}
	for _, pivot := range []bool{false, true} {
		g := New(exec.NewResult("sites", cols, nil), Options{})
		scr := tcell.NewSimulationScreen("UTF-8")
		scr.Init()
		scr.SetSize(60, 10)
		g.SetRect(0, 0, 60, 10)
		if pivot {
			g.TogglePivot()
		}
		g.Draw(scr)
		keys := []tcell.Key{tcell.KeyDown, tcell.KeyUp, tcell.KeyPgDn, tcell.KeyPgUp, tcell.KeyEnd, tcell.KeyHome,
			tcell.KeyRight, tcell.KeyLeft, tcell.KeyEnter, tcell.KeyDown}
		done := make(chan struct{})
		go func() {
			for _, k := range keys {
				g.InputHandler()(tcell.NewEventKey(k, 0, tcell.ModNone), func(p tview.Primitive) {})
				g.Draw(scr)
			}
			for _, r := range "jkgGhl" {
				g.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(p tview.Primitive) {})
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("pivot=%v: key handling on an empty result does not return (UI freeze)", pivot)
		}
	}
}

func TestSingleColumnResultStillNavigates(t *testing.T) {
	rows := []exec.Row{{"1"}, {"2"}, {"3"}}
	g := New(exec.NewResult("count", []exec.ColumnInfo{{Name: "n", Numeric: true}}, rows), Options{})
	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init()
	scr.SetSize(40, 10)
	g.SetRect(0, 0, 40, 10)
	g.Draw(scr)
	g.InputHandler()(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), func(tview.Primitive) {})
	if r, _ := g.CurrentCell(); r != 1 {
		t.Fatalf("Down on a one-column result: record %d, want 1", r)
	}
}
