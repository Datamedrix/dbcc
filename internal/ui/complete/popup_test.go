package complete

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/ui/editor"
)

var _ EditorTarget = (*editor.Editor)(nil)

func simScreen(t *testing.T, w, h int) tcell.SimulationScreen {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	s.SetSize(w, h)
	t.Cleanup(s.Fini)
	return s
}

func row(s tcell.SimulationScreen, y int) string {
	w, _ := s.Size()
	var b strings.Builder
	for x := 0; x < w; x++ {
		r, _, _, _ := s.GetContent(x, y)
		b.WriteRune(r)
	}
	return b.String()
}

func keyEv(k tcell.Key) *tcell.EventKey { return tcell.NewEventKey(k, 0, tcell.ModNone) }
func runeKey(r rune) *tcell.EventKey    { return tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone) }

func items(names ...string) []Item {
	out := make([]Item, len(names))
	for i, n := range names {
		out[i] = Item{Text: n, Kind: KindColumn, Detail: "int"}
	}
	return out
}

func TestPopupKeys(t *testing.T) {
	p := NewPopup()
	if p.HandleKey(keyEv(tcell.KeyDown)) {
		t.Fatal("closed popup consumed a key")
	}
	if p.Open(nil, 0, 0, nil) || p.IsOpen() {
		t.Fatal("empty open")
	}
	var got []Item
	p.Open(items("alpha", "beta", "gamma"), 2, 1, func(it Item) { got = append(got, it) })
	if !p.IsOpen() {
		t.Fatal("not open")
	}
	if !p.HandleKey(keyEv(tcell.KeyDown)) || !p.HandleKey(keyEv(tcell.KeyDown)) || !p.HandleKey(keyEv(tcell.KeyDown)) {
		t.Fatal("down not consumed")
	}
	if it, _ := p.Selected(); it.Text != "gamma" {
		t.Fatalf("selected %v (clamped at the end)", it)
	}
	p.HandleKey(keyEv(tcell.KeyUp))
	if it, _ := p.Selected(); it.Text != "beta" {
		t.Fatalf("selected %v", it)
	}
	if p.HandleKey(runeKey('x')) {
		t.Fatal("typed character must go to the editor")
	}
	if p.HandleKey(keyEv(tcell.KeyBackspace2)) || p.HandleKey(keyEv(tcell.KeyLeft)) {
		t.Fatal("editing keys must go to the editor")
	}
	p.HandleKey(keyEv(tcell.KeyEnter))
	if p.IsOpen() || len(got) != 1 || got[0].Text != "beta" {
		t.Fatalf("enter: open=%v got=%v", p.IsOpen(), got)
	}

	p.Open(items("alpha", "beta"), 0, 0, func(it Item) { got = append(got, it) })
	p.HandleKey(keyEv(tcell.KeyTab))
	if p.IsOpen() || got[1].Text != "alpha" {
		t.Fatalf("tab: %v", got)
	}

	p.Open(items("alpha", "beta"), 0, 0, func(it Item) { got = append(got, it) })
	if !p.HandleKey(keyEv(tcell.KeyEsc)) || p.IsOpen() || len(got) != 2 {
		t.Fatal("esc must close without accepting")
	}

	// PgDn/PgUp move by the visible rows.
	var many []string
	for i := 0; i < 25; i++ {
		many = append(many, string(rune('a'+i)))
	}
	p.Open(items(many...), 0, 0, nil)
	p.HandleKey(keyEv(tcell.KeyPgDn))
	if it, _ := p.Selected(); it.Text != "k" {
		t.Fatalf("pgdn: %v", it)
	}
	p.HandleKey(keyEv(tcell.KeyPgUp))
	if it, _ := p.Selected(); it.Text != "a" {
		t.Fatalf("pgup: %v", it)
	}
}

func TestPopupFilter(t *testing.T) {
	p := NewPopup()
	p.Open([]Item{{Text: "name"}, {Text: "`order`"}, {Text: "NOT"}, {Text: "notes"}}, 0, 0, nil)
	p.HandleKey(keyEv(tcell.KeyDown)) // `order`
	if !p.Filter("n") || len(p.Items()) != 3 {
		t.Fatalf("filter n: %v", p.Items())
	}
	if !p.Filter("no") || texts(p.Items())[0] != "NOT" || len(p.Items()) != 2 {
		t.Fatalf("filter no: %v", p.Items())
	}
	p.HandleKey(keyEv(tcell.KeyDown)) // notes
	if !p.Filter("not") {
		t.Fatal("filter not")
	}
	if it, _ := p.Selected(); it.Text != "notes" {
		t.Fatalf("selection kept: %v", it)
	}
	if !p.Filter("or") || texts(p.Items())[0] != "`order`" {
		t.Fatalf("quoted items match without the quote: %v", p.Items())
	}
	if p.Filter("zzz") || p.IsOpen() {
		t.Fatal("no match must close")
	}
}

func TestPopupDraw(t *testing.T) {
	s := simScreen(t, 40, 12)
	p := NewPopup()
	p.Open([]Item{{Text: "customers", Detail: "table"}, {Text: "orders", Detail: "view"}}, 5, 3, nil)
	p.Draw(s)
	s.Show()
	if r := row(s, 4); !strings.Contains(r, "customers") || !strings.Contains(r, "table") || strings.Index(r, "customers") != 6 {
		t.Fatalf("row below the cursor: %q", r)
	}
	if r := row(s, 5); !strings.Contains(r, "orders") {
		t.Fatalf("second row: %q", r)
	}
	// Selected row style differs.
	_, _, st0, _ := s.GetContent(6, 4)
	_, _, st1, _ := s.GetContent(6, 5)
	if st0 == st1 {
		t.Fatal("selection not highlighted")
	}

	// Near the bottom it opens above the cursor; near the right edge it
	// shifts left.
	s2 := simScreen(t, 20, 6)
	p.Open([]Item{{Text: "alpha"}, {Text: "beta"}}, 18, 5, nil)
	p.Draw(s2)
	s2.Show()
	if !strings.Contains(row(s2, 3), "alpha") || !strings.Contains(row(s2, 4), "beta") {
		t.Fatalf("above: %q / %q", row(s2, 3), row(s2, 4))
	}
	if x, _, w, _ := p.GetRect(); x+w > 20 {
		t.Fatalf("clamped: x=%d w=%d", x, w)
	}

	// Closed popup draws nothing.
	s3 := simScreen(t, 20, 6)
	p.Close()
	p.Draw(s3)
	s3.Show()
	if strings.TrimSpace(row(s3, 1)) != "" {
		t.Fatal("closed popup drew")
	}
}

func TestPopupScroll(t *testing.T) {
	s := simScreen(t, 30, 30)
	p := NewPopup()
	p.MaxRows = 3
	p.Open(items("a1", "a2", "a3", "a4", "a5"), 0, 0, nil)
	for i := 0; i < 4; i++ {
		p.HandleKey(keyEv(tcell.KeyDown))
	}
	p.Draw(s)
	s.Show()
	if !strings.Contains(row(s, 1), "a3") || !strings.Contains(row(s, 3), "a5") {
		t.Fatalf("scrolled: %q %q", row(s, 1), row(s, 3))
	}
}

// edSetup returns an editor with text typed and a drawn layout so
// CursorScreenPos is valid.
func edSetup(t *testing.T, text string) (*editor.Editor, tcell.SimulationScreen) {
	t.Helper()
	e := editor.New()
	s := simScreen(t, 60, 10)
	e.SetRect(0, 0, 60, 10)
	e.Focus(func(tview.Primitive) {})
	e.InsertText(text)
	e.Draw(s)
	return e, s
}

func TestPopupWithEditor(t *testing.T) {
	comp := mysqlCompleter(shopLoader())
	e, _ := edSetup(t, "SELECT c. FROM customers c")
	e.SetCursorOffset(len("SELECT c."))

	p := NewPopup()
	if !p.OpenForEditor(comp, e) {
		t.Fatal("nothing offered")
	}
	if got := texts(p.Items()); !eq(got, []string{"id", "name", "email", "`order`"}) {
		t.Fatalf("items %q", got)
	}
	// Typing goes to the editor and narrows the list.
	if !p.RouteEditorKey(runeKey('e')) {
		t.Fatal("typed key not consumed")
	}
	if e.Text() != "SELECT c.e FROM customers c" {
		t.Fatalf("editor text %q", e.Text())
	}
	if got := texts(p.Items()); !eq(got, []string{"email"}) {
		t.Fatalf("filtered %q", got)
	}
	// Backspace widens again.
	p.RouteEditorKey(keyEv(tcell.KeyBackspace2))
	if got := texts(p.Items()); len(got) != 4 {
		t.Fatalf("after backspace %q", got)
	}
	p.RouteEditorKey(runeKey('o'))
	if !p.RouteEditorKey(keyEv(tcell.KeyEnter)) {
		t.Fatal("enter not consumed")
	}
	if e.Text() != "SELECT c.`order` FROM customers c" || p.IsOpen() {
		t.Fatalf("accepted: %q open=%v", e.Text(), p.IsOpen())
	}
	if e.CursorOffset() != len("SELECT c.`order`") {
		t.Fatalf("cursor %d", e.CursorOffset())
	}

	// A non-identifier character closes the list (and is typed).
	e.SetText("SELECT * FROM o")
	e.SetCursorOffset(len(e.Text()))
	if !p.OpenForEditor(comp, e) || len(p.Items()) != 2 {
		t.Fatalf("open: %v", p.Items())
	}
	p.RouteEditorKey(runeKey(' '))
	if p.IsOpen() || e.Text() != "SELECT * FROM o " {
		t.Fatalf("space: open=%v text=%q", p.IsOpen(), e.Text())
	}

	// Esc closes and leaves the text alone.
	e.SetText("SELECT * FROM o")
	e.SetCursorOffset(len(e.Text()))
	p.OpenForEditor(comp, e)
	p.RouteEditorKey(keyEv(tcell.KeyEsc))
	if p.IsOpen() || e.Text() != "SELECT * FROM o" {
		t.Fatal("esc")
	}

	// Moving the cursor before the word start closes.
	p.OpenForEditor(comp, e)
	p.RouteEditorKey(keyEv(tcell.KeyLeft))
	p.RouteEditorKey(keyEv(tcell.KeyLeft))
	if p.IsOpen() {
		t.Fatal("cursor left the word")
	}

	// Single candidate: inserted directly.
	e.SetText("SELECT * FROM cu")
	e.SetCursorOffset(len(e.Text()))
	if !p.OpenForEditor(comp, e) || p.IsOpen() || e.Text() != "SELECT * FROM Customers" {
		t.Fatalf("single: open=%v text=%q", p.IsOpen(), e.Text())
	}

	// Inside a string: nothing.
	e.SetText("SELECT 'cu")
	e.SetCursorOffset(len(e.Text()))
	if p.OpenForEditor(comp, e) || p.IsOpen() {
		t.Fatal("string")
	}
}

func TestPopupAnchorAtWordStart(t *testing.T) {
	comp := mysqlCompleter(shopLoader())
	e, s := edSetup(t, "SELECT * FROM or")
	p := NewPopup()
	if !p.OpenForEditor(comp, e) {
		t.Fatal("open")
	}
	cx, cy := e.CursorScreenPos()
	p.Draw(s)
	x, y, _, _ := p.GetRect()
	if y != cy+1 || x != cx-2 {
		t.Fatalf("popup at %d,%d; cursor %d,%d", x, y, cx, cy)
	}
}
