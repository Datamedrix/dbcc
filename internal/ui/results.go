package ui

import (
	"fmt"
	"slices"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/exec"
	"dbcc/internal/ui/grid"
)

// ResultTab is one tab of the result area: a grid, a text view or the
// message log (always last).
type ResultTab struct {
	Title string
	Grid  *grid.Grid      // result tabs
	Text  *tview.TextView // cat output, pwd, …
	Log   *MessageLog     // the Messages tab
}

func (t *ResultTab) primitive() tview.Primitive {
	switch {
	case t.Grid != nil:
		return t.Grid
	case t.Text != nil:
		return t.Text
	default:
		return t.Log
	}
}

// ResultTabs is the result area: tab bar plus content (spec 3.3: one tab
// per result plus a message log).
type ResultTabs struct {
	*tview.Flex
	bar     *tview.TextView
	content *tview.Pages
	theme   *Theme
	tabs    []*ResultTab
	cur     int
	seq     int
	// MaxTabs bounds result tabs; the oldest are dropped first.
	MaxTabs int
	// OnChange is called when the current tab changes.
	OnChange func(t *ResultTab)
}

// NewResultTabs creates the area with the Messages tab.
func NewResultTabs(theme *Theme, log *MessageLog) *ResultTabs {
	r := &ResultTabs{
		Flex:    tview.NewFlex().SetDirection(tview.FlexRow),
		bar:     tview.NewTextView().SetDynamicColors(true).SetRegions(true).SetWrap(false),
		content: tview.NewPages(),
		theme:   theme,
		MaxTabs: 20,
	}
	r.AddItem(r.bar, 1, 0, false).AddItem(r.content, 0, 1, true)
	r.tabs = []*ResultTab{{Title: "Messages", Log: log}}
	r.content.AddPage("messages", log, true, true)
	r.render()
	return r
}

// AddResult opens a new tab for a result.
func (r *ResultTabs) AddResult(res *exec.Result, opts grid.Options) *ResultTab {
	g := grid.New(res, opts)
	r.seq++
	t := &ResultTab{Title: fmt.Sprintf("%d %s", r.seq, res.Title), Grid: g}
	r.insert(t)
	return t
}

// AddText opens a text tab. Plain text (no lexer: help, notes) wraps at
// word boundaries; highlighted text (cat output, sources) keeps its layout
// and scrolls horizontally — R6 forbids wrapping only for results.
func (r *ResultTabs) AddText(title, text, lexer string) *ResultTab {
	plain := lexer == ""
	tv := tview.NewTextView().SetDynamicColors(true).SetWrap(plain).SetWordWrap(plain).SetScrollable(true)
	tv.SetText(HighlightTags(text, lexer))
	r.seq++
	t := &ResultTab{Title: fmt.Sprintf("%d %s", r.seq, title), Text: tv}
	r.insert(t)
	return t
}

func (r *ResultTabs) insert(t *ResultTab) {
	// Insert before Messages (always last).
	n := len(r.tabs)
	r.tabs = append(r.tabs[:n-1], t, r.tabs[n-1])
	for len(r.tabs)-1 > r.MaxTabs {
		r.cur = len(r.tabs) - 1
		r.Close(0) // frees the oldest result like a manual close
	}
	r.content.AddPage(pageName(t), t.primitive(), true, false)
	r.Select(len(r.tabs) - 2)
}

func pageName(t *ResultTab) string { return fmt.Sprintf("%p", t) }

// Select shows tab i.
func (r *ResultTabs) Select(i int) {
	if i < 0 || i >= len(r.tabs) {
		return
	}
	r.cur = i
	t := r.tabs[i]
	if t.Log != nil {
		r.content.SwitchToPage("messages")
	} else {
		r.content.SwitchToPage(pageName(t))
	}
	r.render()
	if r.OnChange != nil {
		r.OnChange(t)
	}
}

// Shows reports whether g is the grid of an open result tab (a job that
// finishes after its tab was closed must not fill a detached grid).
func (r *ResultTabs) Shows(g *grid.Grid) bool {
	for _, t := range r.tabs {
		if t.Grid == g && g != nil {
			return true
		}
	}
	return false
}

// Close closes result tab i and frees its memory; the Messages tab can
// never be closed. An open lazy cursor is closed, which releases the main
// session.
func (r *ResultTabs) Close(i int) {
	if i < 0 || i >= len(r.tabs)-1 {
		return
	}
	t := r.tabs[i]
	r.content.RemovePage(pageName(t))
	if t.Grid != nil {
		res := t.Grid.Result()
		t.Grid.SetResult(nil) // the grid drops its source and view
		if res != nil {
			res.Release() // rows and open cursor (releases the main session)
		}
		t.Grid = nil
	}
	t.Text, t.Log = nil, nil
	// slices.Delete zeroes the vacated slot, so the backing array keeps no
	// pointer to the closed tab.
	r.tabs = slices.Delete(r.tabs, i, i+1)
	if r.cur >= i && r.cur > 0 {
		r.cur--
	}
	r.Select(min(r.cur, len(r.tabs)-1))
}

// CloseAll closes every result tab and shows the Messages tab.
func (r *ResultTabs) CloseAll() {
	for len(r.tabs) > 1 {
		r.Close(0)
	}
	r.seq = 0
}

// Next / Prev cycle through tabs.
func (r *ResultTabs) Next() { r.Select((r.cur + 1) % len(r.tabs)) }
func (r *ResultTabs) Prev() { r.Select((r.cur + len(r.tabs) - 1) % len(r.tabs)) }

// ShowMessages selects the Messages tab.
func (r *ResultTabs) ShowMessages() { r.Select(len(r.tabs) - 1) }

// CurrentIndex returns the index of the current tab.
func (r *ResultTabs) CurrentIndex() int { return r.cur }

// Current returns the current tab.
func (r *ResultTabs) Current() *ResultTab { return r.tabs[r.cur] }

// Tabs returns all tabs (Messages last).
func (r *ResultTabs) Tabs() []*ResultTab { return r.tabs }

// CurrentResult returns the result of the current tab, or nil.
func (r *ResultTabs) CurrentResult() *exec.Result {
	if g := r.Current().Grid; g != nil {
		return g.Result()
	}
	return nil
}

func (r *ResultTabs) render() {
	var s string
	for i, t := range r.tabs {
		title := tview.Escape(t.Title)
		if t.Grid != nil {
			if res := t.Grid.Result(); res != nil && res.Incomplete() {
				title += "*"
			}
		}
		if i == r.cur {
			s += fmt.Sprintf("[black:teal] %s [-:-]", title)
		} else {
			s += fmt.Sprintf(" %s ", title)
		}
		if i < len(r.tabs)-1 {
			s += "[gray]│[-]"
		}
	}
	r.bar.SetText(s)
}

// Refresh redraws the tab bar (partial markers).
func (r *ResultTabs) Refresh() { r.render() }

// Focus forwards focus to the current content.
func (r *ResultTabs) Focus(delegate func(p tview.Primitive)) { delegate(r.Current().primitive()) }

// HasFocus reports whether the content has focus.
func (r *ResultTabs) HasFocus() bool { return r.Current().primitive().HasFocus() }

// InputHandler forwards to the current content.
func (r *ResultTabs) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return r.Flex.InputHandler()
}
