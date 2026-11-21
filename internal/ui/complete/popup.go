package complete

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Popup is the completion list (spec 9.2). It opens only on demand (the
// layout calls Open or OpenForEditor on the completion key), filters while
// the user keeps typing into the editor, accepts with Enter or Tab, closes
// with Esc and moves with Up/Down/PgUp/PgDn.
//
// The layout draws it as an overlay after everything else
// (Application.SetAfterDrawFunc) and offers every key to HandleKey (or
// RouteEditorKey) first while IsOpen. Popup must be used from the UI
// goroutine.
type Popup struct {
	*tview.Box

	all, shown []Item
	sel, top   int
	x, y       int // anchor: the cursor cell
	open       bool
	onAccept   func(Item)

	// editor binding (OpenForEditor)
	ed    EditorTarget
	start int // replacement start in the editor text

	// MaxRows is the maximum number of visible rows (default 10).
	MaxRows int
	// MaxWidth caps the popup width in cells (default 60).
	MaxWidth int
	// InsertSingle inserts a single candidate directly instead of showing
	// a one-row list (ShowResult/OpenForEditor; default true).
	InsertSingle bool

	normal, selected, detail tcell.Style
}

// NewPopup returns a closed popup with default styles.
func NewPopup() *Popup {
	return &Popup{
		Box:      tview.NewBox(),
		MaxRows:  10,
		MaxWidth: 60,

		InsertSingle: true,
		normal:       tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorWhite),
		selected:     tcell.StyleDefault.Background(tcell.ColorTeal).Foreground(tcell.ColorWhite).Bold(true),
		detail:       tcell.StyleDefault.Background(tcell.ColorDarkSlateGray).Foreground(tcell.ColorSilver),
	}
}

// SetStyles sets the styles of rows, the selected row and the detail
// column (the detail text uses the background of the row it is on).
func (p *Popup) SetStyles(normal, selected, detail tcell.Style) *Popup {
	p.normal, p.selected, p.detail = normal, selected, detail
	return p
}

// Open shows items with the popup anchored at the screen cell (x, y),
// normally Editor.CursorScreenPos(). onAccept is called with the chosen
// item. Open with no items does nothing and returns false.
func (p *Popup) Open(items []Item, x, y int, onAccept func(Item)) bool {
	p.ed = nil
	if len(items) == 0 {
		p.Close()
		return false
	}
	p.all = items
	p.shown = items
	p.sel, p.top = 0, 0
	p.x, p.y = x, y
	p.onAccept = onAccept
	p.open = true
	return true
}

// Close hides the popup.
func (p *Popup) Close() {
	p.open = false
	p.all, p.shown, p.onAccept, p.ed = nil, nil, nil, nil
}

// IsOpen reports whether the popup is visible.
func (p *Popup) IsOpen() bool { return p.open }

// Items returns the currently shown (filtered) items.
func (p *Popup) Items() []Item { return p.shown }

// Selected returns the selected item.
func (p *Popup) Selected() (Item, bool) {
	if !p.open || p.sel >= len(p.shown) {
		return Item{}, false
	}
	return p.shown[p.sel], true
}

// Filter narrows the items opened with Open to those whose text (without
// an opening quote) starts with prefix, case-insensitively. When nothing
// matches, the popup closes and Filter returns false.
func (p *Popup) Filter(prefix string) bool {
	if !p.open {
		return false
	}
	var cur string
	if it, ok := p.Selected(); ok {
		cur = it.Text
	}
	var out []Item
	for _, it := range p.all {
		if hasPrefixFold(unquoted(it.Text), prefix) || hasPrefixFold(it.Text, prefix) {
			out = append(out, it)
		}
	}
	if len(out) == 0 {
		p.Close()
		return false
	}
	p.shown = out
	p.sel, p.top = 0, 0
	for i, it := range out {
		if it.Text == cur {
			p.sel = i
			break
		}
	}
	p.scrollToSel()
	return true
}

// unquoted strips a leading quote character for filtering.
func unquoted(s string) string {
	if s != "" && strings.IndexByte("`\"[", s[0]) >= 0 {
		return s[1:]
	}
	return s
}

// HandleKey handles navigation, accept and close keys while open and
// reports whether it consumed ev. Other keys (typed characters, Backspace,
// cursor movement) are left to the editor; after the editor handled one,
// call Refilter (or use RouteEditorKey, which does both).
func (p *Popup) HandleKey(ev *tcell.EventKey) bool {
	if !p.open {
		return false
	}
	switch ev.Key() {
	case tcell.KeyUp, tcell.KeyCtrlP:
		p.move(-1)
	case tcell.KeyDown, tcell.KeyCtrlN:
		p.move(1)
	case tcell.KeyPgUp:
		p.move(-p.rows())
	case tcell.KeyPgDn:
		p.move(p.rows())
	case tcell.KeyEnter, tcell.KeyTab:
		p.accept()
	case tcell.KeyEsc:
		p.Close()
	case tcell.KeyCtrlSpace:
		// Completion key while open: keep the list (no-op).
	default:
		return false
	}
	return true
}

func (p *Popup) accept() {
	it, ok := p.Selected()
	fn := p.onAccept
	p.Close()
	if ok && fn != nil {
		fn(it)
	}
}

func (p *Popup) move(d int) {
	n := len(p.shown)
	if n == 0 {
		return
	}
	p.sel += d
	if p.sel < 0 {
		p.sel = 0
	}
	if p.sel >= n {
		p.sel = n - 1
	}
	p.scrollToSel()
}

func (p *Popup) rows() int {
	r := p.MaxRows
	if r <= 0 {
		r = 10
	}
	if len(p.shown) < r {
		r = len(p.shown)
	}
	return r
}

func (p *Popup) scrollToSel() {
	r := p.rows()
	if p.sel < p.top {
		p.top = p.sel
	}
	if p.sel >= p.top+r {
		p.top = p.sel - r + 1
	}
}

// Draw draws the list below the anchor cell (above it when there is no
// room below), clamped to the screen.
func (p *Popup) Draw(screen tcell.Screen) {
	if !p.open || len(p.shown) == 0 {
		return
	}
	sw, sh := screen.Size()
	rows := p.rows()
	textW, detW := 0, 0
	for _, it := range p.shown {
		if w := tview.TaggedStringWidth(tview.Escape(it.Text)); w > textW {
			textW = w
		}
		if w := tview.TaggedStringWidth(tview.Escape(it.Detail)); w > detW {
			detW = w
		}
	}
	w := 1 + textW + 1
	if detW > 0 {
		w += 1 + detW + 1
	}
	maxW := p.MaxWidth
	if maxW <= 0 {
		maxW = 60
	}
	if w > maxW {
		w = maxW
	}
	if w > sw {
		w = sw
	}
	x, y := p.x, p.y+1
	if y+rows > sh && p.y-rows >= 0 {
		y = p.y - rows
	}
	if y+rows > sh {
		rows = sh - y
	}
	if x+w > sw {
		x = sw - w
	}
	if x < 0 {
		x = 0
	}
	if rows <= 0 || w <= 0 {
		return
	}
	p.Box.SetRect(x, y, w, rows)
	for r := 0; r < rows; r++ {
		i := p.top + r
		if i >= len(p.shown) {
			break
		}
		it := p.shown[i]
		st := p.normal
		dst := p.detail
		if i == p.sel {
			st = p.selected
			_, bg, _ := p.selected.Decompose()
			dst = p.detail.Background(bg)
		}
		for cx := 0; cx < w; cx++ {
			screen.SetContent(x+cx, y+r, ' ', nil, st)
		}
		putText(screen, x+1, y+r, w-2, it.Text, st)
		if it.Detail != "" {
			tw := cellWidth(it.Text)
			avail := w - 2 - tw - 2
			if avail > 0 {
				d := it.Detail
				dw := cellWidth(d)
				if dw > avail {
					d = truncate(d, avail)
					dw = cellWidth(d)
				}
				putText(screen, x+w-1-dw, y+r, dw, d, dst)
			}
		}
	}
}

func cellWidth(s string) int { return tview.TaggedStringWidth(tview.Escape(s)) }

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	out := []rune{}
	n := 0
	for _, r := range s {
		rw := cellWidth(string(r))
		if n+rw > w-1 {
			out = append(out, '…')
			return string(out)
		}
		out = append(out, r)
		n += rw
	}
	return string(out)
}

func putText(screen tcell.Screen, x, y, w int, s string, st tcell.Style) {
	if cellWidth(s) > w {
		s = truncate(s, w)
	}
	n := 0
	for _, r := range s {
		rw := cellWidth(string(r))
		if rw < 1 {
			rw = 1
		}
		screen.SetContent(x+n, y, r, nil, st)
		n += rw
		if n >= w {
			break
		}
	}
}

// ---------------------------------------------------------------- editor

// EditorTarget is what the popup needs from the SQL editor;
// *editor.Editor implements it.
type EditorTarget interface {
	Text() string
	CursorOffset() int
	CursorScreenPos() (x, y int)
	ReplaceRange(startByte, endByte int, text string)
	HandleKey(ev *tcell.EventKey) bool
}

// CompleteTimeout bounds the metadata loads of OpenForEditor.
var CompleteTimeout = 3 * time.Second

// OpenForEditor computes the candidates at the editor cursor and opens
// the popup there. It may load metadata synchronously (bounded by
// CompleteTimeout); on a warm cache it is instant. For a fully
// asynchronous variant run comp.Complete in a goroutine and call ShowResult
// from QueueUpdateDraw. It returns false when there is nothing to offer.
func (p *Popup) OpenForEditor(comp *Completer, ed EditorTarget) bool {
	ctx, cancel := context.WithTimeout(context.Background(), CompleteTimeout)
	defer cancel()
	text, cur := ed.Text(), ed.CursorOffset()
	return p.ShowResult(comp.Complete(ctx, text, cur), ed)
}

// ShowResult opens the popup for a Result computed on the current text of
// ed. With exactly one candidate it is inserted directly (no list).
func (p *Popup) ShowResult(res Result, ed EditorTarget) bool {
	cur := ed.CursorOffset()
	if res.Start > cur || len(res.Items) == 0 {
		p.Close()
		return false
	}
	start := res.Start
	accept := func(it Item) { ed.ReplaceRange(start, ed.CursorOffset(), it.Text) }
	if len(res.Items) == 1 && p.InsertSingle {
		accept(res.Items[0])
		p.Close()
		return true
	}
	x, y := ed.CursorScreenPos()
	// Anchor at the start of the word, so the list does not jump while
	// typing.
	x -= utf8.RuneCountInString(ed.Text()[start:cur])
	if !p.Open(res.Items, x, y, accept) {
		return false
	}
	p.ed, p.start = ed, start
	return true
}

// Refilter re-filters the list on the word between the replacement start
// and the editor cursor, after the editor handled a key. It closes the
// popup when the cursor left the word (moved before its start or typed a
// non-identifier character) or nothing matches.
func (p *Popup) Refilter() {
	if !p.open || p.ed == nil {
		return
	}
	text, cur := p.ed.Text(), p.ed.CursorOffset()
	if cur < p.start || cur > len(text) {
		p.Close()
		return
	}
	word := text[p.start:cur]
	if word != "" && strings.IndexByte("`\"[", word[0]) >= 0 {
		word = word[1:]
	}
	for _, r := range word {
		if !(r == '_' || r == '$' || r >= 0x80 || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			p.Close()
			return
		}
	}
	p.Filter(word)
}

// RouteEditorKey is the layout's key hook while the popup is open on an
// editor: the popup handles its keys; any other key goes to the editor
// (which keeps receiving typed characters) and the list is re-filtered.
// It returns true when the key was consumed by either; false means the
// editor did not handle it either (the popup has closed then) and the
// layout should process it normally (e.g. F5).
func (p *Popup) RouteEditorKey(ev *tcell.EventKey) bool {
	if !p.open {
		return false
	}
	if p.HandleKey(ev) {
		return true
	}
	ed := p.ed
	if ed == nil {
		return false
	}
	if !ed.HandleKey(ev) {
		p.Close()
		return false
	}
	p.Refilter()
	return true
}
