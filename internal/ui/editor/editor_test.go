package editor

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/sqltext"
)

func ev(k tcell.Key, mod tcell.ModMask) *tcell.EventKey { return tcell.NewEventKey(k, 0, mod) }
func ctrl(c byte) *tcell.EventKey {
	return tcell.NewEventKey(tcell.KeyCtrlA+tcell.Key(c-'a'), 0, tcell.ModCtrl)
}
func alt(r rune) *tcell.EventKey { return tcell.NewEventKey(tcell.KeyRune, r, tcell.ModAlt) }

func press(t *testing.T, e *Editor, evs ...*tcell.EventKey) {
	t.Helper()
	for _, x := range evs {
		if !e.HandleKey(x) {
			t.Fatalf("key %v not consumed", x.Name())
		}
	}
}

func typ(t *testing.T, e *Editor, s string) {
	t.Helper()
	for _, r := range s {
		if r == '\n' {
			press(t, e, ev(tcell.KeyEnter, 0))
			continue
		}
		press(t, e, tcell.NewEventKey(tcell.KeyRune, r, 0))
	}
}

func withText(s string, line, col int) *Editor {
	e := New()
	e.SetText(s)
	e.SetCursor(line, col)
	return e
}

func expect(t *testing.T, e *Editor, text string, line, col int) {
	t.Helper()
	if got := e.Text(); got != text {
		t.Fatalf("text = %q, want %q", got, text)
	}
	if l, c := e.Cursor(); l != line || c != col {
		t.Fatalf("cursor = %d:%d, want %d:%d", l, c, line, col)
	}
}

func screenFor(t *testing.T, w, h int) tcell.SimulationScreen {
	t.Helper()
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	s.SetSize(w, h)
	return s
}

func draw(t *testing.T, e *Editor, w, h int) tcell.SimulationScreen {
	t.Helper()
	s := screenFor(t, w, h)
	e.SetRect(0, 0, w, h)
	e.Focus(func(p tview.Primitive) {}) // focus for cursor
	e.Draw(s)
	s.Show()
	return s
}

func rowText(s tcell.SimulationScreen, y, w int) string {
	var b strings.Builder
	for x := 0; x < w; x++ {
		r, _, _, _ := s.GetContent(x, y)
		b.WriteRune(r)
	}
	return b.String()
}

func TestTypingMultiline(t *testing.T) {
	e := New()
	typ(t, e, "SELECT 1\n  FROM t\nWHERE")
	expect(t, e, "SELECT 1\n  FROM t\n  WHERE", 3, 7)
	if !e.Dirty() {
		t.Fatal("not dirty")
	}
	press(t, e, ev(tcell.KeyBackspace2, 0), ev(tcell.KeyBackspace, 0))
	expect(t, e, "SELECT 1\n  FROM t\n  WHE", 3, 5)
	press(t, e, ctrl('a'), ev(tcell.KeyBackspace, 0))
	expect(t, e, "SELECT 1\n  FROM t  WHE", 2, 8)
	press(t, e, ev(tcell.KeyDelete, 0), ev(tcell.KeyDelete, 0))
	expect(t, e, "SELECT 1\n  FROM tWHE", 2, 8)
	e.SetClean()
	if e.Dirty() {
		t.Fatal("dirty after SetClean")
	}
	changes := 0
	e.OnChange = func() { changes++ }
	typ(t, e, "x")
	if changes != 1 || !e.Dirty() {
		t.Fatalf("changes=%d dirty=%v", changes, e.Dirty())
	}
	// CRLF normalised
	e.SetText("a\r\nb\rc")
	if e.Text() != "a\nb\nc" || e.LineCount() != 3 {
		t.Fatalf("got %q", e.Text())
	}
}

func TestReadlineMovement(t *testing.T) {
	text := "select foo_bar, baz\nfrom t"
	e := withText(text, 1, 7)
	press(t, e, ctrl('e'))
	expect(t, e, text, 1, 19)
	press(t, e, ctrl('a'))
	expect(t, e, text, 1, 0)
	press(t, e, ctrl('f'), ctrl('f'))
	expect(t, e, text, 1, 2)
	press(t, e, ctrl('b'))
	expect(t, e, text, 1, 1)
	press(t, e, alt('f'))
	expect(t, e, text, 1, 6)
	press(t, e, alt('f'))
	expect(t, e, text, 1, 14) // foo_bar is one word
	press(t, e, alt('b'))
	expect(t, e, text, 1, 7)
	press(t, e, ctrl('n'))
	expect(t, e, text, 2, 6) // clamped to line end
	press(t, e, ctrl('p'))
	expect(t, e, text, 1, 7) // preferred column kept
	press(t, e, ev(tcell.KeyEnd, tcell.ModCtrl))
	expect(t, e, text, 2, 6)
	press(t, e, ev(tcell.KeyHome, tcell.ModCtrl))
	expect(t, e, text, 1, 0)
	press(t, e, alt('>'))
	expect(t, e, text, 2, 6)
	press(t, e, alt('<'))
	expect(t, e, text, 1, 0)
	press(t, e, ev(tcell.KeyEnd, 0))
	expect(t, e, text, 1, 19)
	press(t, e, ev(tcell.KeyRight, 0)) // wraps to next line
	expect(t, e, text, 2, 0)
	press(t, e, ev(tcell.KeyLeft, 0))
	expect(t, e, text, 1, 19)
	press(t, e, ev(tcell.KeyRight, tcell.ModCtrl))
	expect(t, e, text, 2, 4)
	press(t, e, ev(tcell.KeyLeft, tcell.ModCtrl))
	expect(t, e, text, 2, 0)

	// Home toggles between first non-blank and column 0
	e = withText("    x", 1, 5)
	press(t, e, ev(tcell.KeyHome, 0))
	expect(t, e, "    x", 1, 4)
	press(t, e, ev(tcell.KeyHome, 0))
	expect(t, e, "    x", 1, 0)

	// PgUp/PgDn
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "line")
	}
	e = withText(strings.Join(lines, "\n"), 1, 0)
	draw(t, e, 40, 10)
	press(t, e, ev(tcell.KeyPgDn, 0))
	if e.CursorLine() != 10 {
		t.Fatalf("PgDn line = %d", e.CursorLine())
	}
	press(t, e, ev(tcell.KeyPgUp, 0))
	if e.CursorLine() != 1 {
		t.Fatalf("PgUp line = %d", e.CursorLine())
	}
}

func TestReadlineKills(t *testing.T) {
	var copied []string
	e := withText("select a, b from t\nwhere x", 1, 9)
	e.OnCopy = func(s string) { copied = append(copied, s) }
	press(t, e, ctrl('k'))
	expect(t, e, "select a,\nwhere x", 1, 9)
	if e.LastKill() != " b from t" {
		t.Fatalf("kill = %q", e.LastKill())
	}
	press(t, e, ctrl('k')) // at EOL: kills the newline, appends to the same entry
	expect(t, e, "select a,where x", 1, 9)
	if e.LastKill() != " b from t\n" {
		t.Fatalf("kill = %q", e.LastKill())
	}
	press(t, e, ctrl('e'))
	e.PasteLast() // Ctrl+Shift+V without terminal paste
	expect(t, e, "select a,where x b from t\n", 2, 0)

	e = withText("select foo bar", 1, 14)
	press(t, e, ctrl('w'))
	expect(t, e, "select foo ", 1, 11)
	press(t, e, ctrl('w')) // consecutive backward kills prepend
	expect(t, e, "select ", 1, 7)
	if e.LastKill() != "foo bar" {
		t.Fatalf("kill = %q", e.LastKill())
	}
	press(t, e, ctrl('e'), ctrl('u')) // a motion in between starts a new entry
	expect(t, e, "", 1, 0)
	e.PasteLast() // the last kill
	expect(t, e, "select ", 1, 7)
	// Ctrl+Y and Alt+Y were removed (owner request); they are no longer consumed.
	if e.HandleKey(ctrl('y')) || e.HandleKey(alt('w')) {
		t.Fatal("removed keys Ctrl+Y / Alt+W still bound")
	}
	if e.HandleKey(alt('y')) {
		t.Fatal("removed key Alt+Y still bound")
	}

	e = withText("a.b_c d", 1, 0)
	press(t, e, alt('d'))
	expect(t, e, ".b_c d", 1, 0)
	press(t, e, alt('d'))
	expect(t, e, " d", 1, 0)
	e = withText("abc def", 1, 7)
	press(t, e, ev(tcell.KeyBackspace, tcell.ModAlt))
	expect(t, e, "abc ", 1, 4)

	e = withText("xy", 1, 0)
	press(t, e, ctrl('d'))
	expect(t, e, "y", 1, 0)
	press(t, e, ev(tcell.KeyBackspace, 0)) // Ctrl+H arrives as Backspace; nothing before
	expect(t, e, "y", 1, 0)
	if len(copied) == 0 {
		t.Fatal("OnCopy never called")
	}
}

func TestSelection(t *testing.T) {
	e := withText("select 1\nfrom t", 1, 0)
	shift := tcell.ModShift
	press(t, e, ev(tcell.KeyRight, shift), ev(tcell.KeyRight, shift), ev(tcell.KeyRight, shift))
	if s, l, ok := e.Selection(); !ok || s != "sel" || l != 1 {
		t.Fatalf("selection %q %d %v", s, l, ok)
	}
	press(t, e, ev(tcell.KeyEnd, shift))
	if s, _, _ := e.Selection(); s != "select 1" {
		t.Fatalf("selection %q", s)
	}
	press(t, e, ev(tcell.KeyDown, shift))
	if s, _, _ := e.Selection(); s != "select 1\nfrom t" {
		t.Fatalf("selection %q", s)
	}
	press(t, e, ev(tcell.KeyHome, shift|tcell.ModCtrl))
	if e.HasSelection() {
		t.Fatal("selection should collapse back to anchor")
	}
	press(t, e, ev(tcell.KeyEnd, shift|tcell.ModCtrl), ev(tcell.KeyLeft, shift|tcell.ModCtrl))
	if s, _, _ := e.Selection(); s != "select 1\nfrom " {
		t.Fatalf("selection %q", s)
	}
	typ(t, e, "X") // replaces selection
	expect(t, e, "Xt", 1, 1)

	e = withText("abc\ndef", 2, 1)
	press(t, e, alt('a'))
	if s, l, _ := e.Selection(); s != "abc\ndef" || l != 1 {
		t.Fatalf("select all %q", s)
	}
	e.Copy() // Ctrl+Shift+C (routed by the application)
	if e.HasSelection() || e.LastKill() != "abc\ndef" {
		t.Fatal("copy failed")
	}
	press(t, e, ev(tcell.KeyHome, shift))
	press(t, e, ctrl('w')) // cut selection
	expect(t, e, "abc\n", 2, 0)
	press(t, e, ev(tcell.KeyLeft, shift), ev(tcell.KeyBackspace, 0))
	expect(t, e, "abc", 1, 3)
	// plain movement clears the selection
	press(t, e, ev(tcell.KeyLeft, shift), ev(tcell.KeyLeft, 0))
	if e.HasSelection() {
		t.Fatal("selection not cleared")
	}
	expect(t, e, "abc", 1, 2)
	if st, en, ok := e.SelectionRange(); ok {
		t.Fatal(st, en)
	}
}

func TestUndoRedo(t *testing.T) {
	e := New()
	typ(t, e, "select")
	press(t, e, ev(tcell.KeyLeft, 0))
	typ(t, e, "X")
	expect(t, e, "selecXt", 1, 6)
	press(t, e, ctrl('z'))
	expect(t, e, "select", 1, 5)
	press(t, e, tcell.NewEventKey(tcell.KeyCtrlUnderscore, 0, tcell.ModCtrl))
	expect(t, e, "", 1, 0)
	if e.Undo() {
		t.Fatal("undo stack should be empty")
	}
	press(t, e, tcell.NewEventKey(tcell.KeyRune, '_', tcell.ModCtrl)) // CSI-u form, nothing to undo
	expect(t, e, "", 1, 0)
	press(t, e, alt('z'))
	expect(t, e, "select", 1, 5)
	press(t, e, alt('Z'))
	expect(t, e, "selecXt", 1, 6)
	if e.Redo() {
		t.Fatal("redo stack should be empty")
	}

	// paste is one undo step and is not interpreted as keys
	e = withText("x", 1, 1)
	e.PasteHandler()("SELECT 1;\n\tSELECT 2;\x1b[A", func(p tview.Primitive) {})
	expect(t, e, "xSELECT 1;\n\tSELECT 2;\x1b[A", 2, 13)
	press(t, e, ctrl('z'))
	expect(t, e, "x", 1, 1)
	press(t, e, alt('z'))
	expect(t, e, "xSELECT 1;\n\tSELECT 2;\x1b[A", 2, 13)

	// ReplaceAll and ReplaceRange are undoable; SetText resets history
	e = withText("abc", 1, 0)
	e.ReplaceAll("zzz")
	e.ReplaceRange(1, 2, "Y")
	expect(t, e, "zYz", 1, 2)
	e.Undo()
	e.Undo()
	expect(t, e, "abc", 1, 0)
	e.SetText("new")
	if e.Undo() {
		t.Fatal("SetText should clear undo")
	}
	// undo back to the clean text clears dirty
	typ(t, e, "q")
	e.Undo()
	if e.Dirty() {
		t.Fatal("dirty after undo to clean state")
	}
}

func TestUnhandledKeys(t *testing.T) {
	e := withText("abc", 1, 0)
	for _, k := range []*tcell.EventKey{
		ev(tcell.KeyTab, 0), ev(tcell.KeyBacktab, tcell.ModShift), ev(tcell.KeyF5, 0), ev(tcell.KeyEscape, 0),
		tcell.NewEventKey(tcell.KeyCtrlSpace, 0, tcell.ModCtrl), alt('n'), alt(','), alt('.'), alt('x'), alt('r'), alt('1'),
		ctrl('c'), ctrl('o'),
	} {
		if e.HandleKey(k) {
			t.Errorf("%s consumed", k.Name())
		}
	}
	if e.Text() != "abc" {
		t.Fatal(e.Text())
	}
	e.TabInserts = true
	press(t, e, ev(tcell.KeyTab, 0))
	expect(t, e, "    abc", 1, 4)
	press(t, e, alt('i'))
	expect(t, e, "        abc", 1, 8)
	if len(e.Bindings()) == 0 {
		t.Fatal("no bindings")
	}
}

func TestHorizontalScrollNoWrap(t *testing.T) {
	long := strings.Repeat("abcdefghij", 29) + "0123456789" // 300 chars, ends with digits
	e := withText(long+"\nsecond", 1, 0)
	s := draw(t, e, 40, 5)
	if got := rowText(s, 1, 40); !strings.Contains(got, "second") {
		t.Fatalf("row 1 = %q (line wrapped?)", got)
	}
	press(t, e, ev(tcell.KeyEnd, 0))
	s = draw(t, e, 40, 5)
	_, col := e.ScrollOffset()
	if col == 0 {
		t.Fatal("no horizontal scroll")
	}
	row0 := rowText(s, 0, 40)
	if !strings.Contains(row0, "0123456789") {
		t.Fatalf("row 0 = %q", row0)
	}
	if strings.Contains(rowText(s, 1, 40), "abc") {
		t.Fatalf("wrapped: %q", rowText(s, 1, 40))
	}
	cx, cy := e.CursorScreenPos()
	if cy != 0 || cx >= 40 || cx < 4 {
		t.Fatalf("cursor at %d,%d", cx, cy)
	}
	if x, y, _ := s.GetCursor(); x != cx || y != cy {
		t.Fatalf("screen cursor %d,%d vs %d,%d", x, y, cx, cy)
	}
	press(t, e, ev(tcell.KeyHome, 0))
	draw(t, e, 40, 5)
	if _, col := e.ScrollOffset(); col != 0 {
		t.Fatalf("col offset %d after Home", col)
	}
}

func TestHighlightStyles(t *testing.T) {
	for _, lx := range []string{"mysql", "postgresql", "plpgsql", "tsql", "nonsense"} {
		e := New()
		e.SetLexer(lx)
		e.SetText("SELECT 'abc' -- note")
		s := draw(t, e, 40, 3)
		gw := 4 // marker + 2 digits + space
		style := func(col int) tcell.Style {
			_, _, st, _ := s.GetContent(gw+col, 0)
			return st
		}
		kw, str, com := style(0), style(8), style(15)
		if kw == str || kw == com || str == com {
			t.Errorf("%s: styles not distinct: kw=%v str=%v com=%v", lx, kw, str, com)
		}
		st := e.GetStyles()
		if e.StyleAt(1, 0) != st.Keyword || e.StyleAt(1, 8) != st.String || e.StyleAt(1, 15) != st.Comment {
			t.Errorf("%s: StyleAt mismatch", lx)
		}
	}
	// multi-byte and tabs do not shift styles
	e := New()
	e.SetLexer("mysql")
	e.SetText("SELECT 'ä漢'\t-- c\nSELECT 1")
	st := e.GetStyles()
	idx := strings.Index(e.lines[0], "--")
	if e.StyleAt(1, idx) != st.Comment || e.StyleAt(2, 0) != st.Keyword {
		t.Fatal("styles shifted by multi-byte text")
	}
	s := draw(t, e, 40, 3)
	// "SELECT 'ä漢'" = 12 cells (漢 is wide), tab to column 16, comment there
	r, _, cst, _ := s.GetContent(4+16, 0)
	if r != '-' {
		t.Fatalf("comment at %q", r)
	}
	if fg, _, _ := cst.Decompose(); fg != tcell.ColorGray {
		t.Fatalf("comment fg %v", fg)
	}
	// 16-colour degradation keeps keywords distinguishable
	if DefaultStyles(8).Keyword == DefaultStyles(8).Text {
		t.Fatal("8-colour keyword style equals text")
	}
}

func markerRows(t *testing.T, e *Editor, h int) []int {
	s := draw(t, e, 40, h)
	var rows []int
	for y := 0; y < h; y++ {
		if r, _, _, _ := s.GetContent(0, y); r == e.GetStyles().MarkerRune {
			rows = append(rows, y)
		}
	}
	return rows
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCurrentStatementMarker(t *testing.T) {
	e := New()
	e.SetSplitter(sqltext.NewMySQL())
	e.SetText("SELECT 1;\nSELECT\n  2;")
	if rows := markerRows(t, e, 5); !eqInts(rows, []int{0}) {
		t.Fatalf("marker rows %v", rows)
	}
	press(t, e, ev(tcell.KeyDown, 0))
	if rows := markerRows(t, e, 5); !eqInts(rows, []int{1, 2}) {
		t.Fatalf("marker rows %v", rows)
	}
	st, ok := e.CurrentStatement()
	if !ok || st.Line != 2 || st.Text != "SELECT\n  2" {
		t.Fatalf("stmt %+v", st)
	}

	script := "DELIMITER $$\nCREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\nEND$$\nDELIMITER ;\nSELECT 2;"
	e.SetText(script)
	e.SetCursorLine(4)
	st, ok = e.CurrentStatement()
	if !ok || st.Line != 2 || !strings.HasPrefix(st.Text, "CREATE PROCEDURE") || !strings.HasSuffix(st.Text, "END") {
		t.Fatalf("stmt %+v", st)
	}
	if rows := markerRows(t, e, 8); !eqInts(rows, []int{1, 2, 3, 4}) {
		t.Fatalf("marker rows %v", rows)
	}
	e.SetCursorLine(7)
	st, _ = e.CurrentStatement()
	if st.Text != "SELECT 2" {
		t.Fatalf("stmt %+v", st)
	}
	if rows := markerRows(t, e, 8); !eqInts(rows, []int{6}) {
		t.Fatalf("marker rows %v", rows)
	}
	// optional background
	sty := e.GetStyles()
	sty.StatementBg = tcell.ColorNavy
	e.SetStyles(sty)
	s := draw(t, e, 40, 8)
	_, _, cs, _ := s.GetContent(4, 6)
	_, _, os, _ := s.GetContent(4, 1)
	if _, bg, _ := cs.Decompose(); bg != tcell.ColorNavy {
		t.Fatalf("statement bg %v", bg)
	}
	if _, bg, _ := os.Decompose(); bg == tcell.ColorNavy {
		t.Fatal("other statement has bg")
	}
}

func TestUTF8(t *testing.T) {
	e := withText("ä漢x", 1, 0)
	press(t, e, ev(tcell.KeyRight, 0))
	expect(t, e, "ä漢x", 1, 2)
	press(t, e, ev(tcell.KeyRight, 0))
	expect(t, e, "ä漢x", 1, 5)
	s := draw(t, e, 20, 3)
	cx, _ := e.CursorScreenPos()
	if cx != 4+3 { // gutter + ä(1) + 漢(2)
		t.Fatalf("cursor x %d", cx)
	}
	if r, _, _, w := s.GetContent(4+1, 0); r != '漢' || w != 2 {
		t.Fatalf("cell %q width %d", r, w)
	}
	press(t, e, ev(tcell.KeyBackspace, 0))
	expect(t, e, "äx", 1, 2)
	typ(t, e, "漢")
	press(t, e, ev(tcell.KeyLeft, 0), ev(tcell.KeyLeft, 0))
	expect(t, e, "ä漢x", 1, 0)
	// combining sequence is one cluster
	e = withText("äb", 1, 0)
	press(t, e, ev(tcell.KeyRight, 0))
	expect(t, e, "äb", 1, 3)
	// vertical move keeps the display column across wide characters
	e = withText("漢漢x\nabcde", 1, 0)
	press(t, e, ev(tcell.KeyEnd, 0), ev(tcell.KeyDown, 0))
	expect(t, e, "漢漢x\nabcde", 2, 5)
	e.SetCursor(2, 2)
	press(t, e, ev(tcell.KeyUp, 0))
	expect(t, e, "漢漢x\nabcde", 1, 3)
	// word before cursor with multi-byte identifiers
	e = withText("SELECT * FROM db.täble", 1, len("SELECT * FROM db.täble"))
	w, start := e.WordBeforeCursor()
	if w != "täble" || start != len("SELECT * FROM db.") {
		t.Fatalf("word %q at %d", w, start)
	}
	if e.TextBeforeCursor() != "SELECT * FROM db.täble" {
		t.Fatal(e.TextBeforeCursor())
	}
	e.ReplaceRange(start, start+len(w), "`täble x`")
	expect(t, e, "SELECT * FROM db.`täble x`", 1, len("SELECT * FROM db.`täble x`"))
}

func TestTabs(t *testing.T) {
	tb := NewTabs()
	tb.SetSplitter(sqltext.NewMySQL())
	tb.SetLexer("mysql")
	changes := 0
	tb.OnChange = func() { changes++ }
	tb.AddScratch("", "")
	obj := struct{ name string }{"calc_total"}
	a := tb.OpenObject("db.calc_total", "calc_total", "CREATE PROCEDURE calc_total() SELECT 1", obj)
	tb.AddScratch("", "")
	if got := tb.BarText(); got != "[scratch]-[calc_total]-[scratch2]" {
		t.Fatalf("bar %q", got)
	}
	b := tb.OpenObject("db.calc_total", "calc_total", "other", nil)
	if a != b || tb.Len() != 3 || tb.CurrentIndex() != 1 || tb.Current() != a {
		t.Fatal("open twice did not focus the existing tab")
	}
	if a.Object != any(obj) || a.Editor.Text() != "CREATE PROCEDURE calc_total() SELECT 1" {
		t.Fatal("object binding lost")
	}
	if !tb.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'X', 0)) {
		t.Fatal("key not forwarded")
	}
	if !a.Dirty() || tb.BarText() != "[scratch]-[calc_total*]-[scratch2]" || !tb.AnyDirty() {
		t.Fatalf("dirty marker missing: %q", tb.BarText())
	}
	if !a.Restore() || a.Editor.Text() != a.Original || a.Dirty() {
		t.Fatal("restore failed")
	}
	a.Editor.Undo()
	if a.Editor.Text() != "XCREATE PROCEDURE calc_total() SELECT 1" {
		t.Fatal("restore not undoable")
	}
	if tb.Tabs()[0].Restore() {
		t.Fatal("scratch restore")
	}
	tb.Next()
	tb.Next()
	if tb.CurrentIndex() != 0 {
		t.Fatal(tb.CurrentIndex())
	}
	tb.Prev()
	if tb.CurrentIndex() != 2 {
		t.Fatal(tb.CurrentIndex())
	}
	tb.Close(2)
	if tb.CurrentIndex() != 1 || tb.Len() != 2 {
		t.Fatal("close")
	}
	tb.Close(0)
	if tb.Current() != a {
		t.Fatal("close before current")
	}
	if changes == 0 {
		t.Fatal("OnChange not called")
	}
	// drawing: bar on top, editor below with cursor
	s := screenFor(t, 40, 5)
	tb.SetRect(0, 0, 40, 5)
	tb.Focus(func(p tview.Primitive) {})
	tb.Draw(s)
	s.Show()
	if got := strings.TrimRight(rowText(s, 0, 40), " "); got != "[calc_total*]" {
		t.Fatalf("bar row %q", got)
	}
	if !strings.Contains(rowText(s, 1, 40), "XCREATE") {
		t.Fatalf("editor row %q", rowText(s, 1, 40))
	}
	if _, y, vis := s.GetCursor(); !vis || y != 1 {
		t.Fatal("cursor not shown in editor")
	}
	tb.PasteHandler()("p\nq", func(p tview.Primitive) {})
	if !strings.Contains(a.Editor.Text(), "p\nq") {
		t.Fatal("paste not forwarded")
	}
}
