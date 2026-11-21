package editor

import (
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Binding is one editor key for the help overlay.
type Binding struct {
	Key  string
	Help string
}

// Bindings lists the text-editing keys handled by the editor.
func (e *Editor) Bindings() []Binding {
	b := []Binding{
		{"Left/Right, Ctrl+B/Ctrl+F", "Character left / right"},
		{"Alt+B/Alt+F, Ctrl+Left/Right", "Word left / right"},
		{"Up/Down, Ctrl+P/Ctrl+N", "Line up / down"},
		{"Home/End, Ctrl+A/Ctrl+E", "Line start / end (Home toggles first non-blank)"},
		{"PgUp/PgDn", "Page up / down"},
		{"Ctrl+Home/End, Alt+</Alt+>", "Buffer start / end"},
		{"Shift+movement", "Extend the selection"},
		{"Alt+A", "Select all"},
		{"Ctrl+Shift+C", "Copy the selection to the clipboard (also Ctrl+C)"},
		{"Ctrl+Shift+X", "Cut the selection to the clipboard (also Ctrl+X)"},
		{"Ctrl+Shift+V", "Paste (the terminal's paste; otherwise the last copied text)"},
		{"Ctrl+W", "Kill word backward, or cut the selection"},
		{"Ctrl+K", "Kill to end of line"},
		{"Ctrl+U", "Kill to line start"},
		{"Alt+D", "Kill word forward"},
		{"Alt+Backspace", "Kill word backward"},
		{"Ctrl+Z, Ctrl+_", "Undo"},
		{"Alt+Z", "Redo"},
		{"Ctrl+D, Delete", "Delete character"},
		{"Backspace", "Delete character backward"},
		{"Enter", "New line, keeping indentation"},
		{"Alt+I", "Insert indentation"},
	}
	if e.TabInserts {
		b = append(b, Binding{"Tab", "Insert indentation"})
	}
	return b
}

// InputHandler implements tview.Primitive.
func (e *Editor) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return e.WrapInputHandler(func(ev *tcell.EventKey, _ func(p tview.Primitive)) {
		e.HandleKey(ev)
	})
}

// PasteHandler implements tview.Primitive: pasted text is inserted as one
// undo step and never interpreted as keys.
func (e *Editor) PasteHandler() func(text string, setFocus func(p tview.Primitive)) {
	return e.WrapPasteHandler(func(text string, _ func(p tview.Primitive)) {
		e.InsertText(text)
		e.resetCmd()
	})
}

// HandleKey processes one key event and reports whether the editor
// consumed it. Unhandled keys (Tab, F-keys, Esc, Ctrl+Space, unbound Alt
// and Ctrl combinations) return false.
func (e *Editor) HandleKey(ev *tcell.EventKey) bool {
	e.thisCmd = cmdOther
	keepEdit := false
	ok := e.handle(ev, &keepEdit)
	if ok {
		e.lastCmd = e.thisCmd
		if !keepEdit {
			e.lastEdit = editNone
		}
	}
	return ok
}

func (e *Editor) handle(ev *tcell.EventKey, keepEdit *bool) bool {
	mod := ev.Modifiers()
	shift := mod&tcell.ModShift != 0
	ctrl := mod&tcell.ModCtrl != 0
	alt := mod&tcell.ModAlt != 0

	switch ev.Key() {
	case tcell.KeyRune:
		r := ev.Rune()
		if alt {
			return e.altRune(r)
		}
		if ctrl {
			if r == '_' || r == '/' { // Ctrl+_ / Ctrl+/ in the CSI-u key protocol
				e.Undo()
				return true
			}
			return false
		}
		*keepEdit = true
		e.typeText(string(r))
		return true
	case tcell.KeyLeft:
		if ctrl || alt {
			e.wordMove(false, shift)
		} else {
			e.charLeft(shift)
		}
	case tcell.KeyRight:
		if ctrl || alt {
			e.wordMove(true, shift)
		} else {
			e.charRight(shift)
		}
	case tcell.KeyUp:
		e.vertical(-1, shift)
	case tcell.KeyDown:
		e.vertical(1, shift)
	case tcell.KeyPgUp:
		e.vertical(-e.page(), shift)
	case tcell.KeyPgDn:
		e.vertical(e.page(), shift)
	case tcell.KeyHome:
		if ctrl {
			e.moveTo(pos{}, shift)
		} else {
			e.smartHome(shift)
		}
	case tcell.KeyEnd:
		if ctrl {
			last := len(e.lines) - 1
			e.moveTo(pos{last, len(e.lines[last])}, shift)
		} else {
			e.moveTo(pos{e.cur.row, len(e.lines[e.cur.row])}, shift)
		}
	case tcell.KeyCtrlA:
		e.moveTo(pos{e.cur.row, 0}, shift)
	case tcell.KeyCtrlE:
		e.moveTo(pos{e.cur.row, len(e.lines[e.cur.row])}, shift)
	case tcell.KeyCtrlB:
		e.charLeft(shift)
	case tcell.KeyCtrlF:
		e.charRight(shift)
	case tcell.KeyCtrlP:
		e.vertical(-1, shift)
	case tcell.KeyCtrlN:
		e.vertical(1, shift)
	case tcell.KeyEnter:
		e.newline()
	case tcell.KeyTab:
		if !e.TabInserts || shift || ctrl || alt {
			return false
		}
		e.indent()
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if alt {
			e.killWordBackward()
		} else {
			*keepEdit = true
			e.backspace()
		}
	case tcell.KeyDelete, tcell.KeyCtrlD:
		*keepEdit = true
		e.deleteForward()
	case tcell.KeyCtrlK:
		e.killToEOL()
	case tcell.KeyCtrlU:
		e.killToBOL()
	case tcell.KeyCtrlW:
		if a, b, ok := e.selRange(); ok {
			e.killRange(e.offsetOf(a), e.offsetOf(b), true)
		} else {
			off := e.offsetOf(e.cur)
			e.killRange(unixWordBack(e.Text(), off), off, false)
		}
	case tcell.KeyCtrlZ, tcell.KeyCtrlUnderscore:
		e.Undo()
	default:
		return false
	}
	return true
}

func (e *Editor) altRune(r rune) bool {
	switch unicode.ToLower(r) {
	case 'b':
		e.wordMove(false, false)
	case 'f':
		e.wordMove(true, false)
	case 'd':
		off := e.offsetOf(e.cur)
		e.killRange(off, forwardWord(e.Text(), off), true)
	case 'a':
		e.SelectAll()
	case 'z':
		e.Redo()
	case 'i':
		e.indent()
	case '<':
		e.moveTo(pos{}, false)
	case '>':
		last := len(e.lines) - 1
		e.moveTo(pos{last, len(e.lines[last])}, false)
	default:
		return false
	}
	return true
}

// ---- motions ----

func (e *Editor) moveTo(p pos, extend bool) {
	if extend {
		if !e.selecting {
			e.selecting, e.anchor = true, e.cur
		}
	} else {
		e.selecting = false
	}
	e.cur = e.clamp(p)
	e.wantX, e.follow = -1, true
}

func (e *Editor) charLeft(extend bool) {
	if a, _, ok := e.selRange(); ok && !extend {
		e.moveTo(a, false)
		return
	}
	p := e.cur
	if p.col > 0 {
		p.col = prevBoundary(e.lines[p.row], p.col)
	} else if p.row > 0 {
		p.row--
		p.col = len(e.lines[p.row])
	}
	e.moveTo(p, extend)
}

func (e *Editor) charRight(extend bool) {
	if _, b, ok := e.selRange(); ok && !extend {
		e.moveTo(b, false)
		return
	}
	p := e.cur
	if p.col < len(e.lines[p.row]) {
		p.col = nextBoundary(e.lines[p.row], p.col)
	} else if p.row < len(e.lines)-1 {
		p.row++
		p.col = 0
	}
	e.moveTo(p, extend)
}

func (e *Editor) wordMove(forward, extend bool) {
	text, off := e.Text(), e.offsetOf(e.cur)
	if forward {
		off = forwardWord(text, off)
	} else {
		off = backwardWord(text, off)
	}
	e.moveTo(e.posOf(off), extend)
}

func (e *Editor) vertical(n int, extend bool) {
	x := e.wantX
	if x < 0 {
		x = e.displayCol(e.cur.row, e.cur.col)
	}
	row := e.cur.row + n
	if row < 0 {
		row = 0
	}
	if row >= len(e.lines) {
		row = len(e.lines) - 1
	}
	if n < -1 || n > 1 { // page: scroll the view along
		e.rowOff += n
		if e.rowOff < 0 {
			e.rowOff = 0
		}
	}
	e.moveTo(pos{row, e.colForX(row, x)}, extend)
	e.wantX = x
}

func (e *Editor) page() int {
	if e.textH > 1 {
		return e.textH - 1
	}
	return 1
}

func (e *Editor) smartHome(extend bool) {
	line := e.lines[e.cur.row]
	first := len(line) - len(strings.TrimLeft(line, " \t"))
	col := first
	if e.cur.col == first {
		col = 0
	}
	e.moveTo(pos{e.cur.row, col}, extend)
}

// ---- edits ----

func (e *Editor) typeText(s string) {
	kind := editInsert
	if e.HasSelection() {
		kind = editOther
	}
	e.beginEdit(kind)
	e.insertAtCursor(s)
	e.endEdit()
	if kind == editOther {
		e.lastEdit = editInsert // following typing joins the replacement
	}
}

func (e *Editor) newline() {
	line := e.lines[e.cur.row]
	end := e.cur.col
	if a, _, ok := e.selRange(); ok {
		line, end = e.lines[a.row], a.col
	}
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	if len(indent) > end {
		indent = indent[:end]
	}
	e.beginEdit(editOther)
	e.insertAtCursor("\n" + indent)
	e.endEdit()
}

func (e *Editor) indent() {
	w := e.tabW()
	n := w - e.displayCol(e.cur.row, e.cur.col)%w
	e.beginEdit(editOther)
	e.insertAtCursor(strings.Repeat(" ", n))
	e.endEdit()
}

func (e *Editor) backspace() {
	if e.HasSelection() {
		e.beginEdit(editOther)
		e.deleteSelection()
		e.endEdit()
		return
	}
	e.selecting = false
	p := e.cur
	var a pos
	switch {
	case p.col > 0:
		a = pos{p.row, prevBoundary(e.lines[p.row], p.col)}
	case p.row > 0:
		a = pos{p.row - 1, len(e.lines[p.row-1])}
	default:
		return
	}
	e.beginEdit(editBackspace)
	e.cur = e.replace(a, p, "")
	e.endEdit()
}

func (e *Editor) deleteForward() {
	if e.HasSelection() {
		e.beginEdit(editOther)
		e.deleteSelection()
		e.endEdit()
		return
	}
	e.selecting = false
	p := e.cur
	var b pos
	switch {
	case p.col < len(e.lines[p.row]):
		b = pos{p.row, nextBoundary(e.lines[p.row], p.col)}
	case p.row < len(e.lines)-1:
		b = pos{p.row + 1, 0}
	default:
		return
	}
	e.beginEdit(editDelete)
	e.cur = e.replace(p, b, "")
	e.endEdit()
}

// ---- kill ring ----

func (e *Editor) pushKill(text string, appendTo, forward bool) {
	if appendTo && e.lastCmd == cmdKill && len(e.kills) > 0 {
		i := len(e.kills) - 1
		if forward {
			e.kills[i] += text
		} else {
			e.kills[i] = text + e.kills[i]
		}
		text = e.kills[i]
	} else {
		e.kills = append(e.kills, text)
		if len(e.kills) > maxKills {
			e.kills = e.kills[len(e.kills)-maxKills:]
		}
	}
	if e.OnCopy != nil {
		e.OnCopy(text)
	}
}

// killRange removes bytes [from,to) of Text() into the kill ring.
func (e *Editor) killRange(from, to int, forward bool) {
	e.thisCmd = cmdKill
	if from >= to {
		return
	}
	a, b := e.posOf(from), e.posOf(to)
	text := e.textRange(a, b)
	e.pushKill(text, true, forward)
	e.beginEdit(editOther)
	e.selecting = false
	e.cur = e.replace(a, b, "")
	e.endEdit()
}

func (e *Editor) killToEOL() {
	off := e.offsetOf(e.cur)
	if e.cur.col == len(e.lines[e.cur.row]) {
		if e.cur.row < len(e.lines)-1 {
			e.killRange(off, off+1, true)
		} else {
			e.thisCmd = cmdKill
		}
		return
	}
	e.killRange(off, off+len(e.lines[e.cur.row])-e.cur.col, true)
}

func (e *Editor) killToBOL() {
	off := e.offsetOf(e.cur)
	e.killRange(off-e.cur.col, off, false)
}

func (e *Editor) killWordBackward() {
	off := e.offsetOf(e.cur)
	e.killRange(backwardWord(e.Text(), off), off, false)
}

func (e *Editor) yank() {
	if len(e.kills) == 0 {
		return
	}
	e.yankIdx = len(e.kills) - 1
	e.beginEdit(editOther)
	if a, _, ok := e.selRange(); ok {
		e.yankFrom = e.offsetOf(a)
	} else {
		e.yankFrom = e.offsetOf(e.cur)
	}
	e.insertAtCursor(e.kills[e.yankIdx])
	e.yankTo = e.offsetOf(e.cur)
	e.endEdit()
	e.thisCmd = cmdYank
}

func (e *Editor) yankPop() {
	if e.lastCmd != cmdYank || len(e.kills) < 2 {
		if e.lastCmd == cmdYank {
			e.thisCmd = cmdYank
		}
		return
	}
	e.yankIdx = (e.yankIdx - 1 + len(e.kills)) % len(e.kills)
	e.beginEdit(editYankPop)
	e.selecting = false
	e.cur = e.replace(e.posOf(e.yankFrom), e.posOf(e.yankTo), e.kills[e.yankIdx])
	e.yankTo = e.offsetOf(e.cur)
	e.endEdit()
	e.thisCmd = cmdYank
}

// Copy puts the selection into the copy buffer and calls OnCopy (the
// application copies it to the system clipboard). Owner request: Ctrl+Shift+C.
func (e *Editor) Copy() bool {
	text, _, ok := e.Selection()
	if !ok {
		return false
	}
	e.pushKill(text, false, true)
	e.selecting = false
	return true
}

// Cut removes the selection into the copy buffer and calls OnCopy (the
// application copies it to the system clipboard). Owner request: Ctrl+Shift+X.
func (e *Editor) Cut() bool {
	a, b, ok := e.selRange()
	if !ok {
		return false
	}
	e.killRange(e.offsetOf(a), e.offsetOf(b), true)
	e.selecting = false
	return true
}

// PasteLast inserts the last copied or killed text (Ctrl+Shift+V when the
// terminal does not paste itself).
func (e *Editor) PasteLast() bool {
	if len(e.kills) == 0 {
		return false
	}
	e.yank()
	return true
}
