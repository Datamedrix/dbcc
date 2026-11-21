package ui

import (
	"runtime"
	"strings"
	"testing"
	"weak"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/exec"
	"dbcc/internal/ui/grid"
)

func TestCloseResultTabs(t *testing.T) {
	th := NewTheme(256)
	r := NewResultTabs(th, NewMessageLog(th))
	cols := []exec.ColumnInfo{{Name: "a"}}
	for _, title := range []string{"one", "two", "three"} {
		r.AddResult(exec.NewResult(title, cols, []exec.Row{{"1"}}), grid.Options{})
	}
	if len(r.Tabs()) != 4 || r.CurrentIndex() != 2 {
		t.Fatalf("tabs %d, current %d", len(r.Tabs()), r.CurrentIndex())
	}
	r.Close(1) // "two"
	if len(r.Tabs()) != 3 || r.Tabs()[1].Title != "3 three" {
		t.Fatalf("after close: %v", titles(r))
	}
	r.Close(len(r.Tabs()) - 1) // Messages cannot be closed
	if len(r.Tabs()) != 3 {
		t.Fatal("Messages tab was closed")
	}
	r.CloseAll()
	if len(r.Tabs()) != 1 || r.Current().Log == nil {
		t.Fatalf("after close all: %v", titles(r))
	}
	k := DefaultKeymap()
	if a := k.Action(PaneGrid, tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModAlt)); a != "close_result" {
		t.Errorf("alt+x = %q", a)
	}
	if a := k.Action(PaneGrid, tcell.NewEventKey(tcell.KeyRune, 'X', tcell.ModAlt|tcell.ModShift)); a != "close_all_results" {
		t.Errorf("alt+X = %q", a)
	}
}

func titles(r *ResultTabs) []string {
	var out []string
	for _, t := range r.Tabs() {
		out = append(out, t.Title)
	}
	return out
}

// TestCloseFreesMemory proves that a closed result tab is garbage
// collected: nothing (tabs, pages, grid) keeps the result or its rows.
func TestCloseFreesMemory(t *testing.T) {
	th := NewTheme(256)
	r := NewResultTabs(th, NewMessageLog(th))
	var weakRes weak.Pointer[exec.Result]
	var weakRow weak.Pointer[string]
	func() {
		cols := []exec.ColumnInfo{{Name: "payload"}}
		rows := make([]exec.Row, 20000)
		for i := range rows {
			rows[i] = exec.Row{strings.Repeat("x", 100)}
		}
		big := new(string)
		*big = strings.Repeat("y", 1<<20)
		rows[0] = exec.Row{*big}
		res := exec.NewResult("big", cols, rows)
		weakRes = weak.Make(res)
		weakRow = weak.Make(big)
		r.AddResult(res, grid.Options{})
		r.AddText("text", strings.Repeat("z", 1000), "")
	}()
	scr := tcell.NewSimulationScreen("UTF-8")
	scr.Init()
	scr.SetSize(80, 20)
	r.SetRect(0, 0, 80, 20)
	r.Select(0)
	r.Draw(scr) // the grid has rendered and sized its columns
	r.Close(0)
	r.Close(0)
	for i := 0; i < 5 && weakRes.Value() != nil; i++ {
		runtime.GC()
	}
	if weakRes.Value() != nil {
		t.Fatal("closed result is still reachable (memory not freed)")
	}
	_ = weakRow
	if len(r.Tabs()) != 1 || r.Current().Log == nil {
		t.Fatalf("only Messages must remain: %v", titles(r))
	}
}

func TestMaxTabsDropsOldestAndFrees(t *testing.T) {
	th := NewTheme(256)
	r := NewResultTabs(th, NewMessageLog(th))
	r.MaxTabs = 3
	var first weak.Pointer[exec.Result]
	func() {
		res := exec.NewResult("first", []exec.ColumnInfo{{Name: "a"}}, []exec.Row{{"1"}})
		first = weak.Make(res)
		r.AddResult(res, grid.Options{})
	}()
	for i := 0; i < 3; i++ {
		r.AddResult(exec.NewResult("n", []exec.ColumnInfo{{Name: "a"}}, []exec.Row{{"1"}}), grid.Options{})
	}
	if len(r.Tabs()) != 4 {
		t.Fatalf("tabs: %v", titles(r))
	}
	for i := 0; i < 5 && first.Value() != nil; i++ {
		runtime.GC()
	}
	if first.Value() != nil {
		t.Fatal("dropped oldest result still reachable")
	}
}
