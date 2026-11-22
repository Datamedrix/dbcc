package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// The selected node is drawn black on white, whatever its own color.
func TestTreeSelectedNodeBlackOnWhite(t *testing.T) {
	for _, c := range []tcell.Color{tcell.ColorDefault, tcell.ColorGray, tcell.ColorTeal} {
		tr := &Tree{TreeView: tview.NewTreeView()}
		root := tview.NewTreeNode("root")
		n := tview.NewTreeNode("node").SetColor(c)
		root.AddChild(n)
		tr.SetRoot(root).SetTopLevel(1).SetCurrentNode(n)

		scr := tcell.NewSimulationScreen("")
		if err := scr.Init(); err != nil {
			t.Fatal(err)
		}
		scr.SetSize(20, 3)
		tr.SetRect(0, 0, 20, 3)
		tr.Draw(scr)
		_, _, st, _ := scr.GetContent(0, 0)
		fg, bg, _ := st.Decompose()
		if fg != tcell.ColorBlack || bg != tcell.ColorWhite {
			t.Errorf("color %v: selected fg=%v bg=%v, want black on white", c, fg, bg)
		}
		scr.Fini()
	}
}
