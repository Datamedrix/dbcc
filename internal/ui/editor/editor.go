// Package editor implements the SQL editor of dbcc (spec 9.1): a custom
// tview primitive with syntax highlighting, selection, undo/redo,
// Readline-style keys, a line-number gutter, current-statement marking and
// bracketed paste, plus the editor tab container and scratch persistence.
//
// The editor handles text-editing keys only. Pane actions (run statement,
// run all, completion, tab switching, …) belong to the layout and its
// keymap; keys the editor does not handle are left alone (HandleKey reports
// false) so the layout can act on them. In particular Tab and Shift+Tab are
// never consumed unless TabInserts is set, because the layout uses them to
// cycle the focus.
//
// Key bindings (see Bindings for the list shown in the help overlay):
//
//	Movement       Left/Right, Ctrl+B/Ctrl+F           char
//	               Alt+B/Alt+F, Ctrl+Left/Ctrl+Right   word
//	               Up/Down, Ctrl+P/Ctrl+N              line
//	               Home/End, Ctrl+A/Ctrl+E             line start/end (Home toggles first non-blank)
//	               PgUp/PgDn                           page
//	               Ctrl+Home/Ctrl+End, Alt+</Alt+>     buffer start/end
//	Selection      Shift + any of the movement keys above that has an arrow,
//	               Home/End, PgUp/PgDn or Ctrl+Home/End form; Alt+A selects all.
//	               Typing, Enter, Backspace, Delete or paste replace the selection.
//	Kill / yank    Ctrl+K kill to end of line (joins lines at EOL)
//	               Ctrl+U kill to line start
//	               Ctrl+W kill word backward (whitespace-delimited); with a selection: cut
//	               Alt+D kill word forward, Alt+Backspace kill word backward
//	               Alt+W copy the selection into the kill ring
//	               Ctrl+Y yank (Readline), Alt+Y cycle the yanked text (yank-pop)
//	               Consecutive kills accumulate into one kill-ring entry.
//	Undo / redo    Ctrl+Z or Ctrl+_ undo, Alt+Z redo. Ctrl+Y is yank, as in
//	               Readline; Ctrl+Shift+Z cannot be distinguished in terminals.
//	Editing        Enter newline keeping indentation, Backspace/Ctrl+H, Delete/Ctrl+D,
//	               Alt+I insert indentation (TabWidth spaces); Tab does the same only
//	               when TabInserts is true.
//
// The kill ring is internal; OnCopy is called with every text that enters
// it so the layout can forward it to the system clipboard (OSC 52).
package editor

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"

	"dbcc/internal/sqltext"
)

// pos is a buffer position: row index and byte offset within the line.
type pos struct{ row, col int }

func (p pos) less(q pos) bool { return p.row < q.row || p.row == q.row && p.col < q.col }

type snapshot struct {
	lines []string
	cur   pos
}

type editKind int

const (
	editNone editKind = iota
	editInsert
	editDelete
	editBackspace
	editOther
	editYankPop
)

type cmdKind int

const (
	cmdOther cmdKind = iota
	cmdKill
	cmdYank
)

const maxKills = 16
const maxUndo = 1000

// Editor is the SQL editor primitive.
type Editor struct {
	*tview.Box

	// TabInserts makes Tab insert indentation instead of leaving it to the
	// layout for focus cycling. Default false.
	TabInserts bool
	// TabWidth is the indentation width and the display width of a tab
	// character. Default 4.
	TabWidth int
	// OnChange is called after every change of the text.
	OnChange func()
	// OnCopy is called with every text that enters the kill ring.
	OnCopy func(text string)

	lines     []string
	cur       pos
	anchor    pos
	selecting bool
	wantX     int // preferred display column for vertical moves, -1 = none

	rowOff, colOff int
	follow         bool // scroll to the cursor on the next draw

	undo, redo []snapshot
	lastEdit   editKind

	version     int
	cleanText   string
	dirtyVer    int
	dirtyVal    bool
	tabsHook    func()
	drawFocused bool

	kills    []string
	lastCmd  cmdKind
	thisCmd  cmdKind
	yankFrom int // byte offsets of the last yank
	yankTo   int
	yankIdx  int

	lexerName string
	lexer     chroma.Lexer
	styles    Styles
	hlVer     int
	hl        [][]tcell.Style

	splitter sqltext.Splitter
	stmtVer  int
	stmtOff  int
	stmt     sqltext.Statement
	stmtOK   bool

	// layout of the last draw
	textX, textY, textW, textH int
}

// New returns an empty editor using the generic SQL lexer and the default
// 256-colour styles.
func New() *Editor {
	e := &Editor{
		Box:      tview.NewBox(),
		TabWidth: 4,
		lines:    []string{""},
		wantX:    -1,
		styles:   DefaultStyles(256),
		hlVer:    -1,
		stmtVer:  -1,
		dirtyVer: -1,
		follow:   true,
		textH:    10,
		textW:    80,
	}
	e.SetLexer("sql")
	return e
}

// ---- text access ----

// Text returns the whole buffer; lines are joined with "\n".
func (e *Editor) Text() string { return strings.Join(e.lines, "\n") }

func normalize(s string) string {
	if strings.IndexByte(s, '\r') < 0 {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// SetText replaces the buffer, moves the cursor to the start, clears undo
// history and selection and marks the buffer clean.
func (e *Editor) SetText(s string) {
	e.lines = strings.Split(normalize(s), "\n")
	e.cur, e.selecting, e.wantX = pos{}, false, -1
	e.rowOff, e.colOff, e.follow = 0, 0, true
	e.undo, e.redo, e.lastEdit = nil, nil, editNone
	e.cleanText = e.Text()
	e.changed()
}

// ReplaceAll replaces the whole buffer as one undoable step. The cursor
// keeps its line where possible.
func (e *Editor) ReplaceAll(s string) {
	e.beginEdit(editOther)
	e.lines = strings.Split(normalize(s), "\n")
	e.selecting = false
	e.cur = e.clamp(e.cur)
	e.endEdit()
}

// Dirty reports whether the text differs from the last clean state.
func (e *Editor) Dirty() bool {
	if e.dirtyVer != e.version {
		e.dirtyVal = e.Text() != e.cleanText
		e.dirtyVer = e.version
	}
	return e.dirtyVal
}

// SetClean marks the current text as saved.
func (e *Editor) SetClean() {
	e.cleanText = e.Text()
	e.dirtyVer = -1
	e.notify()
}

// LineCount returns the number of lines (at least 1).
func (e *Editor) LineCount() int { return len(e.lines) }

// CursorLine returns the 1-based line of the cursor.
func (e *Editor) CursorLine() int { return e.cur.row + 1 }

// Cursor returns the 1-based line and the 0-based byte column of the cursor.
func (e *Editor) Cursor() (line, col int) { return e.cur.row + 1, e.cur.col }

// SetCursorLine moves the cursor to the start of 1-based line n (clamped),
// clears the selection and scrolls it into view.
func (e *Editor) SetCursorLine(n int) {
	e.SetCursor(n, 0)
}

// SetCursor moves the cursor to the 1-based line and byte column (clamped
// to a character boundary).
func (e *Editor) SetCursor(line, col int) {
	e.selecting = false
	e.cur = e.clamp(pos{line - 1, col})
	e.wantX, e.follow = -1, true
	e.resetCmd()
}

// CursorOffset returns the byte offset of the cursor in Text().
func (e *Editor) CursorOffset() int { return e.offsetOf(e.cur) }

// SetCursorOffset moves the cursor to byte offset off of Text().
func (e *Editor) SetCursorOffset(off int) {
	e.selecting = false
	e.cur = e.posOf(off)
	e.wantX, e.follow = -1, true
	e.resetCmd()
}

func (e *Editor) resetCmd() { e.lastEdit, e.lastCmd = editNone, cmdOther }

// ---- selection ----

func (e *Editor) selRange() (pos, pos, bool) {
	if !e.selecting || e.anchor == e.cur {
		return pos{}, pos{}, false
	}
	a, b := e.anchor, e.cur
	if b.less(a) {
		a, b = b, a
	}
	return a, b, true
}

// HasSelection reports whether a non-empty selection exists.
func (e *Editor) HasSelection() bool { _, _, ok := e.selRange(); return ok }

// Selection returns the selected text and the 1-based line where it starts.
func (e *Editor) Selection() (text string, startLine int, ok bool) {
	a, b, ok := e.selRange()
	if !ok {
		return "", 0, false
	}
	return e.textRange(a, b), a.row + 1, true
}

// SelectionRange returns the byte offsets of the selection in Text().
func (e *Editor) SelectionRange() (start, end int, ok bool) {
	a, b, ok := e.selRange()
	if !ok {
		return 0, 0, false
	}
	return e.offsetOf(a), e.offsetOf(b), true
}

// SelectAll selects the whole buffer.
func (e *Editor) SelectAll() {
	e.anchor = pos{}
	e.selecting = true
	last := len(e.lines) - 1
	e.cur = pos{last, len(e.lines[last])}
	e.follow = true
}

// ClearSelection removes the selection, keeping the cursor.
func (e *Editor) ClearSelection() { e.selecting = false }

// ---- completion hooks ----

// TextBeforeCursor returns the buffer text from the start to the cursor.
func (e *Editor) TextBeforeCursor() string { return e.Text()[:e.offsetOf(e.cur)] }

// WordBeforeCursor returns the identifier fragment directly before the
// cursor (letters, digits, '_' and '$') and its byte offset in Text().
func (e *Editor) WordBeforeCursor() (word string, startByte int) {
	line := e.lines[e.cur.row][:e.cur.col]
	i := len(line)
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(line[:i])
		if !isWord(r) {
			break
		}
		i -= n
	}
	return line[i:], e.offsetOf(pos{e.cur.row, i})
}

// CursorScreenPos returns the screen position of the cursor as of the
// last draw (for placing the completion popup).
func (e *Editor) CursorScreenPos() (x, y int) {
	return e.textX + e.displayCol(e.cur.row, e.cur.col) - e.colOff, e.textY + e.cur.row - e.rowOff
}

// ReplaceRange replaces bytes [startByte, endByte) of Text() with text as
// one undoable step and puts the cursor after the inserted text.
func (e *Editor) ReplaceRange(startByte, endByte int, text string) {
	if endByte < startByte {
		startByte, endByte = endByte, startByte
	}
	e.beginEdit(editOther)
	e.selecting = false
	e.cur = e.replace(e.posOf(startByte), e.posOf(endByte), normalize(text))
	e.endEdit()
}

// InsertText inserts text at the cursor (replacing the selection) as one
// undoable step. Used for paste.
func (e *Editor) InsertText(text string) {
	e.beginEdit(editOther)
	e.insertAtCursor(normalize(text))
	e.endEdit()
}

// LastKill returns the most recent kill-ring entry.
func (e *Editor) LastKill() string {
	if len(e.kills) == 0 {
		return ""
	}
	return e.kills[len(e.kills)-1]
}

// ---- undo ----

// Undo reverts the last edit step. It reports whether anything changed.
func (e *Editor) Undo() bool {
	if len(e.undo) == 0 {
		return false
	}
	e.redo = append(e.redo, e.snap())
	s := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.restore(s)
	return true
}

// Redo re-applies the last undone step.
func (e *Editor) Redo() bool {
	if len(e.redo) == 0 {
		return false
	}
	e.undo = append(e.undo, e.snap())
	s := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	e.restore(s)
	return true
}

func (e *Editor) snap() snapshot {
	return snapshot{lines: append([]string(nil), e.lines...), cur: e.cur}
}

func (e *Editor) restore(s snapshot) {
	e.lines = append([]string(nil), s.lines...)
	e.cur = e.clamp(s.cur)
	e.selecting, e.wantX, e.follow = false, -1, true
	e.lastEdit = editNone
	e.changed()
}

// beginEdit records an undo snapshot unless the edit continues the
// previous one (consecutive typing or deleting).
func (e *Editor) beginEdit(kind editKind) {
	coalesce := kind == editYankPop ||
		(kind == e.lastEdit && (kind == editInsert || kind == editDelete || kind == editBackspace))
	if !coalesce || len(e.undo) == 0 {
		e.undo = append(e.undo, e.snap())
		if len(e.undo) > maxUndo {
			e.undo = e.undo[len(e.undo)-maxUndo:]
		}
	}
	e.redo = nil
	e.lastEdit = kind
}

func (e *Editor) endEdit() {
	e.wantX, e.follow = -1, true
	e.changed()
}

func (e *Editor) changed() {
	e.version++
	e.notify()
}

func (e *Editor) notify() {
	if e.OnChange != nil {
		e.OnChange()
	}
	if e.tabsHook != nil {
		e.tabsHook()
	}
}

// ---- buffer primitives ----

func (e *Editor) offsetOf(p pos) int {
	off := 0
	for i := 0; i < p.row && i < len(e.lines); i++ {
		off += len(e.lines[i]) + 1
	}
	return off + p.col
}

func (e *Editor) posOf(off int) pos {
	if off < 0 {
		off = 0
	}
	for i, l := range e.lines {
		if off <= len(l) {
			return e.clamp(pos{i, off})
		}
		off -= len(l) + 1
	}
	last := len(e.lines) - 1
	return pos{last, len(e.lines[last])}
}

// clamp limits p to the buffer and snaps the column to a grapheme boundary.
func (e *Editor) clamp(p pos) pos {
	if p.row < 0 {
		p.row = 0
	}
	if p.row >= len(e.lines) {
		p.row = len(e.lines) - 1
	}
	line := e.lines[p.row]
	if p.col < 0 {
		p.col = 0
	}
	if p.col > len(line) {
		p.col = len(line)
	}
	// snap down to a boundary
	b, prev := 0, 0
	for b < p.col {
		prev = b
		b = nextBoundary(line, b)
	}
	if b != p.col {
		p.col = prev
	}
	return p
}

func (e *Editor) textRange(a, b pos) string {
	if a.row == b.row {
		return e.lines[a.row][a.col:b.col]
	}
	var sb strings.Builder
	sb.WriteString(e.lines[a.row][a.col:])
	for r := a.row + 1; r < b.row; r++ {
		sb.WriteByte('\n')
		sb.WriteString(e.lines[r])
	}
	sb.WriteByte('\n')
	sb.WriteString(e.lines[b.row][:b.col])
	return sb.String()
}

// replace deletes [a,b) and inserts text; returns the end of the insertion.
func (e *Editor) replace(a, b pos, text string) pos {
	if b.less(a) {
		a, b = b, a
	}
	head := e.lines[a.row][:a.col]
	tail := e.lines[b.row][b.col:]
	ins := strings.Split(text, "\n")
	newLines := make([]string, 0, len(ins))
	for i, s := range ins {
		if i == 0 {
			s = head + s
		}
		newLines = append(newLines, s)
	}
	end := pos{a.row + len(ins) - 1, len(newLines[len(newLines)-1])}
	newLines[len(newLines)-1] += tail
	out := make([]string, 0, len(e.lines)-(b.row-a.row+1)+len(newLines))
	out = append(out, e.lines[:a.row]...)
	out = append(out, newLines...)
	out = append(out, e.lines[b.row+1:]...)
	e.lines = out
	return end
}

func (e *Editor) deleteSelection() bool {
	a, b, ok := e.selRange()
	if !ok {
		e.selecting = false
		return false
	}
	e.cur = e.replace(a, b, "")
	e.selecting = false
	return true
}

func (e *Editor) insertAtCursor(text string) {
	if a, b, ok := e.selRange(); ok {
		e.cur = e.replace(a, b, text)
	} else {
		e.cur = e.replace(e.cur, e.cur, text)
	}
	e.selecting = false
}

// ---- grapheme and width helpers ----

func nextBoundary(line string, col int) int {
	if col >= len(line) {
		return len(line)
	}
	c, _, _, _ := uniseg.FirstGraphemeClusterInString(line[col:], -1)
	if c == "" {
		return col + 1
	}
	return col + len(c)
}

func prevBoundary(line string, col int) int {
	b, prev := 0, 0
	for b < col && b < len(line) {
		prev = b
		b = nextBoundary(line, b)
	}
	return prev
}

func (e *Editor) tabW() int {
	if e.TabWidth <= 0 {
		return 4
	}
	return e.TabWidth
}

// clusterWidth returns the display width of cluster c starting at display
// column x.
func (e *Editor) clusterWidth(c string, w, x int) int {
	if c == "\t" {
		t := e.tabW()
		return t - x%t
	}
	if w <= 0 {
		return 1
	}
	return w
}

// displayCol returns the display column of byte col in row.
func (e *Editor) displayCol(row, col int) int {
	line := e.lines[row]
	x, b, state := 0, 0, -1
	for b < col && b < len(line) {
		c, _, w, ns := uniseg.FirstGraphemeClusterInString(line[b:], state)
		state = ns
		x += e.clusterWidth(c, w, x)
		b += len(c)
	}
	return x
}

// colForX returns the byte column in row whose cluster covers display x
// (or the line end).
func (e *Editor) colForX(row, target int) int {
	line := e.lines[row]
	x, b, state := 0, 0, -1
	for b < len(line) {
		c, _, w, ns := uniseg.FirstGraphemeClusterInString(line[b:], state)
		state = ns
		cw := e.clusterWidth(c, w, x)
		if x+cw > target {
			return b
		}
		x += cw
		b += len(c)
	}
	return len(line)
}

func isWord(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// ---- word motions over the whole text (byte offsets) ----

func forwardWord(s string, i int) int {
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if isWord(r) {
			break
		}
		i += n
	}
	for i < len(s) {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !isWord(r) {
			break
		}
		i += n
	}
	return i
}

func backwardWord(s string, i int) int {
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:i])
		if isWord(r) {
			break
		}
		i -= n
	}
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:i])
		if !isWord(r) {
			break
		}
		i -= n
	}
	return i
}

// unixWordBack implements Ctrl+W: skip whitespace, then non-whitespace.
func unixWordBack(s string, i int) int {
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:i])
		if !unicode.IsSpace(r) {
			break
		}
		i -= n
	}
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:i])
		if unicode.IsSpace(r) {
			break
		}
		i -= n
	}
	return i
}

// ---- current statement ----

// SetSplitter sets the statement splitter used for current-statement
// marking and CurrentStatement. nil disables marking.
func (e *Editor) SetSplitter(s sqltext.Splitter) {
	e.splitter = s
	e.stmtVer = -1
}

// CurrentStatement returns the statement at the cursor as found by the
// splitter. Offsets and line refer to Text().
func (e *Editor) CurrentStatement() (sqltext.Statement, bool) {
	if e.splitter == nil {
		return sqltext.Statement{}, false
	}
	off := e.offsetOf(e.cur)
	if e.stmtVer != e.version || e.stmtOff != off {
		e.stmt, e.stmtOK = e.splitter.StatementAt(e.Text(), off)
		e.stmtVer, e.stmtOff = e.version, off
	}
	return e.stmt, e.stmtOK
}

// markClean records text as the saved state (the buffer may have changed
// meanwhile; then it stays dirty).
func (e *Editor) markClean(text string) {
	e.cleanText = text
	e.dirtyVer = -1
}
