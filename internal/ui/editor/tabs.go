package editor

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"

	"dbcc/internal/sqltext"
)

// Kind is the kind of an editor tab.
type Kind int

const (
	// KindScratch is a scratch tab for the owner's own SQL; persisted.
	KindScratch Kind = iota
	// KindObject holds the replace script of a database object.
	KindObject
)

// Tab is one editor tab.
type Tab struct {
	Title    string
	Kind     Kind
	Key      string // object identity (object tabs)
	Object   any    // caller's object binding (object tabs)
	Original string // source as loaded (object tabs), for Restore
	Conn     string // connection a scratch belongs to (NoConnection for none)
	File     string // scratch file name in the connection's scratch folder
	Editor   *Editor
}

// Dirty reports whether the tab has unsaved changes.
func (t *Tab) Dirty() bool { return t.Editor.Dirty() }

// Restore puts the source as loaded back into an object tab as an undoable
// step. It reports false for scratch tabs.
func (t *Tab) Restore() bool {
	if t.Kind != KindObject {
		return false
	}
	t.Editor.ReplaceAll(t.Original)
	return true
}

// Label returns the tab bar label: the title plus "*" when dirty.
func (t *Tab) Label() string {
	if t.Dirty() {
		return t.Title + "*"
	}
	return t.Title
}

// Tabs is the editor tab container: a one-line tab bar such as
// "[scratch]-[calc_total*]" above the current editor. Keys, paste and mouse
// events go to the current editor; the container itself takes the focus.
// The caller asks before closing dirty tabs; the container never asks.
type Tabs struct {
	*tview.Box

	// OnChange is called after tab switches, opens, closes and edits.
	OnChange func()
	// Configure, if set, is applied to every new editor (after lexer,
	// splitter and styles).
	Configure func(*Editor)

	tabs       []*Tab
	cur        int
	scratchDir string
	lexer      string
	splitter   sqltext.Splitter
	styles     *Styles

	BarStyle, BarCurrentStyle tcell.Style
	spans                     [][2]int // screen x ranges of labels from the last draw
	barY                      int
}

// NewTabs returns an empty tab container.
func NewTabs() *Tabs {
	return &Tabs{
		Box:             tview.NewBox(),
		lexer:           "sql",
		BarStyle:        tcell.StyleDefault.Foreground(tcell.ColorGray),
		BarCurrentStyle: tcell.StyleDefault.Bold(true).Reverse(true),
	}
}

// SetLexer sets the lexer for all current and future editors.
func (t *Tabs) SetLexer(name string) {
	t.lexer = name
	for _, tab := range t.tabs {
		tab.Editor.SetLexer(name)
	}
}

// SetSplitter sets the splitter for all current and future editors.
func (t *Tabs) SetSplitter(s sqltext.Splitter) {
	t.splitter = s
	for _, tab := range t.tabs {
		tab.Editor.SetSplitter(s)
	}
}

// SetStyles sets the styles for all current and future editors.
func (t *Tabs) SetStyles(s Styles) {
	t.styles = &s
	for _, tab := range t.tabs {
		tab.Editor.SetStyles(s)
	}
}

func (t *Tabs) newEditor(text string) *Editor {
	e := New()
	e.SetLexer(t.lexer)
	e.SetSplitter(t.splitter)
	if t.styles != nil {
		e.SetStyles(*t.styles)
	}
	if t.Configure != nil {
		t.Configure(e)
	}
	e.SetText(text)
	e.tabsHook = t.changed
	return e
}

func (t *Tabs) changed() {
	if t.OnChange != nil {
		t.OnChange()
	}
}

func (t *Tabs) add(tab *Tab) *Tab {
	t.tabs = append(t.tabs, tab)
	t.cur = len(t.tabs) - 1
	t.changed()
	return tab
}

// AddScratch adds a scratch tab and makes it current. An empty title is
// replaced by "scratch", "scratch2", ….
func (t *Tabs) AddScratch(title, text string) *Tab {
	if title == "" {
		title = t.freeScratchTitle()
	}
	return t.add(&Tab{Title: title, Kind: KindScratch, Editor: t.newEditor(text)})
}

func (t *Tabs) freeScratchTitle() string {
	used := map[string]bool{}
	for _, tab := range t.tabs {
		used[tab.Title] = true
	}
	if !used["scratch"] {
		return "scratch"
	}
	for n := 2; ; n++ {
		if s := fmt.Sprintf("scratch%d", n); !used[s] {
			return s
		}
	}
}

// OpenObject opens an object tab, or focuses the existing tab with the same
// key (its text is left alone).
func (t *Tabs) OpenObject(key, title, text string, obj any) *Tab {
	for i, tab := range t.tabs {
		if tab.Kind == KindObject && tab.Key == key {
			t.SetCurrent(i)
			return tab
		}
	}
	return t.add(&Tab{Title: title, Kind: KindObject, Key: key, Object: obj, Original: text, Editor: t.newEditor(text)})
}

// Current returns the current tab, or nil if there are none.
func (t *Tabs) Current() *Tab {
	if t.cur < 0 || t.cur >= len(t.tabs) {
		return nil
	}
	return t.tabs[t.cur]
}

// CurrentIndex returns the index of the current tab (-1 if none).
func (t *Tabs) CurrentIndex() int {
	if len(t.tabs) == 0 {
		return -1
	}
	return t.cur
}

// Tabs returns all tabs in order.
func (t *Tabs) Tabs() []*Tab { return append([]*Tab(nil), t.tabs...) }

// Len returns the number of tabs.
func (t *Tabs) Len() int { return len(t.tabs) }

// SetCurrent makes tab i current.
func (t *Tabs) SetCurrent(i int) {
	if i < 0 || i >= len(t.tabs) {
		return
	}
	t.cur = i
	t.changed()
}

// Next makes the next tab current (wrapping).
func (t *Tabs) Next() {
	if len(t.tabs) > 0 {
		t.SetCurrent((t.cur + 1) % len(t.tabs))
	}
}

// Prev makes the previous tab current (wrapping).
func (t *Tabs) Prev() {
	if len(t.tabs) > 0 {
		t.SetCurrent((t.cur - 1 + len(t.tabs)) % len(t.tabs))
	}
}

// Close removes tab i without asking. The container may become empty.
func (t *Tabs) Close(i int) {
	if i < 0 || i >= len(t.tabs) {
		return
	}
	t.tabs[i].Editor.tabsHook = nil
	t.tabs = append(t.tabs[:i], t.tabs[i+1:]...)
	if t.cur > i || t.cur >= len(t.tabs) {
		t.cur--
	}
	if t.cur < 0 {
		t.cur = 0
	}
	t.changed()
}

// Index returns the index of tab, or -1.
func (t *Tabs) Index(tab *Tab) int {
	for i, x := range t.tabs {
		if x == tab {
			return i
		}
	}
	return -1
}

// AnyDirty reports whether any tab has unsaved changes.
func (t *Tabs) AnyDirty() bool {
	for _, tab := range t.tabs {
		if tab.Dirty() {
			return true
		}
	}
	return false
}

// Draw implements tview.Primitive.
func (t *Tabs) Draw(screen tcell.Screen) {
	t.Box.DrawForSubclass(screen, t)
	x, y, w, h := t.GetInnerRect()
	if w <= 0 || h <= 0 {
		return
	}
	t.drawBar(screen, x, y, w)
	if cur := t.Current(); cur != nil && h > 1 {
		cur.Editor.SetRect(x, y+1, w, h-1)
		cur.Editor.drawFocused = t.HasFocus()
		cur.Editor.Draw(screen)
	}
}

// BarText returns the tab bar text, e.g. "[scratch]-[calc_total*]".
func (t *Tabs) BarText() string {
	s := ""
	for i, tab := range t.tabs {
		if i > 0 {
			s += "-"
		}
		s += "[" + tab.Label() + "]"
	}
	return s
}

func (t *Tabs) drawBar(screen tcell.Screen, x, y, w int) {
	bg := t.GetBackgroundColor()
	withBg := func(st tcell.Style) tcell.Style {
		if _, b, _ := st.Decompose(); b == tcell.ColorDefault {
			return st.Background(bg)
		}
		return st
	}
	labels := make([]string, len(t.tabs))
	widths := make([]int, len(t.tabs))
	for i, tab := range t.tabs {
		labels[i] = "[" + tab.Label() + "]"
		widths[i] = uniseg.StringWidth(labels[i])
	}
	// first visible tab: scroll so the current one fits
	first := 0
	for first < t.cur {
		total := 0
		for i := first; i <= t.cur; i++ {
			total += widths[i] + 1
		}
		if total-1 <= w {
			break
		}
		first++
	}
	t.spans = make([][2]int, len(t.tabs))
	t.barY = y
	cx := x
	for i := first; i < len(t.tabs) && cx < x+w; i++ {
		if i > first {
			screen.SetContent(cx, y, '-', nil, withBg(t.BarStyle))
			cx++
		}
		st := t.BarStyle
		if i == t.cur {
			st = t.BarCurrentStyle
		}
		start := cx
		state := -1
		rest := labels[i]
		for rest != "" && cx < x+w {
			var c string
			var cw int
			c, rest, cw, state = uniseg.FirstGraphemeClusterInString(rest, state)
			if cx+cw > x+w {
				break
			}
			rs := []rune(c)
			screen.SetContent(cx, y, rs[0], rs[1:], withBg(st))
			cx += cw
		}
		t.spans[i] = [2]int{start, cx}
	}
}

// Focus implements tview.Primitive. The container keeps the focus itself
// and forwards input to the current editor.
func (t *Tabs) Focus(delegate func(p tview.Primitive)) { t.Box.Focus(delegate) }

// InputHandler implements tview.Primitive.
func (t *Tabs) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return t.WrapInputHandler(func(ev *tcell.EventKey, _ func(p tview.Primitive)) {
		t.HandleKey(ev)
	})
}

// HandleKey forwards ev to the current editor and reports whether it was
// consumed.
func (t *Tabs) HandleKey(ev *tcell.EventKey) bool {
	if cur := t.Current(); cur != nil {
		return cur.Editor.HandleKey(ev)
	}
	return false
}

// PasteHandler implements tview.Primitive.
func (t *Tabs) PasteHandler() func(text string, setFocus func(p tview.Primitive)) {
	return t.WrapPasteHandler(func(text string, _ func(p tview.Primitive)) {
		if cur := t.Current(); cur != nil {
			cur.Editor.InsertText(text)
			cur.Editor.resetCmd()
		}
	})
}

// MouseHandler implements tview.Primitive: clicks on the bar switch tabs.
func (t *Tabs) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return t.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(p tview.Primitive)) (bool, tview.Primitive) {
		mx, my := ev.Position()
		if !t.InRect(mx, my) {
			return false, nil
		}
		if my == t.barY {
			if action == tview.MouseLeftClick || action == tview.MouseLeftDown {
				setFocus(t)
				for i, sp := range t.spans {
					if mx >= sp[0] && mx < sp[1] {
						t.SetCurrent(i)
						break
					}
				}
			}
			return true, nil
		}
		cur := t.Current()
		if cur == nil {
			return false, nil
		}
		consumed, capture := cur.Editor.MouseHandler()(action, ev, func(p tview.Primitive) { setFocus(t) })
		if capture != nil {
			capture = t
		}
		return consumed, capture
	})
}
