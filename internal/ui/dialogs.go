package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/exec"
	"dbcc/internal/script"
	"dbcc/internal/ui/editor"
	"dbcc/internal/ui/grid"
	"dbcc/internal/ui/splash"
)

// ---------------------------------------------------------------- host

// host implements script.Host and exec.Sink for the TUI. Its methods are
// called from the worker goroutine and hand over to the UI goroutine.
type host struct{ a *App }

var _ script.Host = (*host)(nil)

func (h *host) Result(r *exec.Result) {
	a := h.a
	// Table-origin results carry their metadata (pivot headers show the
	// primary key, forms and filters need the columns). Runs on the worker.
	if o := r.Origin; o != nil && o.Table == nil && o.Object.Object != "" && a.meta != nil {
		p := o.Object
		if p.Conn == "" {
			if s := a.ws.Current(); s != nil {
				p.Conn = s.Profile.Name
			}
		}
		if t, err := a.meta.Table(context.Background(), p); err == nil {
			o.Table = t
		}
	}
	a.tv.QueueUpdateDraw(func() {
		t := a.results.AddResult(r, a.gridOptionsFor(r))
		if p := a.pane(); p == PaneCmd || p == PaneTree || p == PaneGrid {
			a.tv.SetFocus(t.primitive())
		}
	})
}

func (h *host) Text(title, text, lexer string) {
	a := h.a
	if lexer == "sql" {
		if s := a.ws.Current(); s != nil {
			lexer = s.Dialect.LexerName()
		}
	}
	a.tv.QueueUpdateDraw(func() {
		t := a.results.AddText(title, text, lexer)
		if p := a.pane(); p == PaneCmd || p == PaneTree || p == PaneGrid {
			a.tv.SetFocus(t.primitive())
		}
	})
}

func (h *host) Message(m exec.Message) {
	a := h.a
	a.tv.QueueUpdateDraw(func() {
		a.log.Add(m)
		if m.Err != nil {
			a.lastErr = oneLine(m.Err.Error())
		}
		if m.Duration > 0 {
			a.mu.Lock()
			a.lastDur = m.Duration
			a.mu.Unlock()
		}
	})
}

func (h *host) Confirm(msg string) (bool, error) {
	ch := make(chan bool, 1)
	h.a.tv.QueueUpdateDraw(func() {
		h.a.modalButtons("confirm", msg, []string{"Yes", "No"}, func(i int) { ch <- i == 0 })
	})
	select {
	case v := <-ch:
		return v, nil
	case <-h.a.quit:
		return false, script.ErrNoUI
	}
}

func (h *host) Pick(title string, items []string) (int, error) {
	ch := make(chan int, 1)
	h.a.tv.QueueUpdateDraw(func() { h.a.pickList(title, items, func(i int) { ch <- i }) })
	select {
	case v := <-ch:
		return v, nil
	case <-h.a.quit:
		return -1, script.ErrNoUI
	}
}

func (h *host) EditorSet(text string) error {
	h.a.ui(func() {
		ed := h.a.editors
		var tab *editor.Tab
		if cur := ed.Current(); cur != nil && cur.Kind == editor.KindScratch {
			tab = cur
		} else {
			for _, t := range ed.Tabs() {
				if t.Kind == editor.KindScratch {
					tab = t
					break
				}
			}
		}
		if tab == nil {
			tab = ed.AddScratch("", "")
		}
		ed.SetCurrent(ed.Index(tab))
		tab.Editor.ReplaceAll(text)
		h.a.tv.SetFocus(ed)
	})
	return nil
}

func (h *host) EditorOpen(title, text string, obj *script.ObjectRef) error {
	h.a.ui(func() {
		ed := h.a.editors
		if obj == nil {
			t := ed.AddScratch(title, text)
			ed.SetCurrent(ed.Index(t))
		} else {
			key := string(obj.Kind) + ":" + obj.Path.String() + "/" + obj.Path.Object
			ed.OpenObject(key, title, text, obj)
		}
		h.a.tv.SetFocus(ed)
	})
	return nil
}

func (h *host) ActiveResult() (*exec.Result, exec.View) {
	var res *exec.Result
	var view exec.View
	h.a.ui(func() {
		res = h.a.results.CurrentResult()
		if t := h.a.results.Current(); t.Grid != nil {
			if v, ok := any(t.Grid).(interface{ View() exec.View }); ok {
				view = v.View()
			}
		}
	})
	return res, view
}

func (h *host) About() error {
	h.a.tv.QueueUpdateDraw(h.a.showAbout)
	return nil
}

// ---------------------------------------------------------------- modals

// modal shows p centred on top of the main page.
func (a *App) modal(name string, p tview.Primitive, width, height int) {
	grid := tview.NewGrid().SetColumns(0, width, 0).SetRows(0, height, 0).AddItem(p, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(name, grid, true, true)
	a.modalStack = append(a.modalStack, name)
	a.tv.SetFocus(p)
}

// closeModal removes a modal and restores the focus.
func (a *App) closeModal(name string, focus tview.Primitive) {
	a.pages.RemovePage(name)
	for i, n := range a.modalStack {
		if n == name {
			a.modalStack = append(a.modalStack[:i], a.modalStack[i+1:]...)
			break
		}
	}
	if focus != nil {
		a.tv.SetFocus(focus)
	} else if len(a.modalStack) == 0 {
		a.focusDefault()
	}
}

// modalButtons shows a message with buttons; done receives the index
// (Esc = last button).
func (a *App) modalButtons(name, msg string, buttons []string, done func(int)) {
	prev := a.tv.GetFocus()
	m := tview.NewModal().SetText(msg).AddButtons(buttons)
	m.SetDoneFunc(func(i int, _ string) {
		a.closeModal(name, prev)
		if i < 0 {
			i = len(buttons) - 1
		}
		done(i)
	})
	m.SetBorderColor(a.theme.BorderFocus)
	a.pages.AddPage(name, m, true, true)
	a.modalStack = append(a.modalStack, name)
	a.tv.SetFocus(m)
}

// confirmUI asks yes/no on the UI goroutine and runs yes on Yes.
func (a *App) confirmUI(msg string, yes func()) {
	a.modalButtons("confirm", msg, []string{"Yes", "No"}, func(i int) {
		if i == 0 {
			yes()
		}
	})
}

// pickList shows a filterable list; done receives the index or -1.
func (a *App) pickList(title string, items []string, done func(int)) {
	prev := a.tv.GetFocus()
	list := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true)
	filter := tview.NewInputField().SetLabel("filter: ").SetFieldBackgroundColor(tcell.ColorDefault)
	var shown []int
	fill := func(f string) {
		list.Clear()
		shown = shown[:0]
		for i, it := range items {
			if f == "" || strings.Contains(strings.ToLower(it), strings.ToLower(f)) {
				shown = append(shown, i)
				list.AddItem(tview.Escape(it), "", 0, nil)
			}
		}
	}
	fill("")
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(filter, 1, 0, true).AddItem(list, 0, 1, false)
	box.SetBorder(true).SetTitle(" " + title + " ").SetBorderColor(a.theme.BorderFocus)
	finish := func(i int) {
		a.closeModal("pick", prev)
		done(i)
	}
	filter.SetChangedFunc(fill)
	filter.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn:
			list.InputHandler()(ev, nil)
			return nil
		case tcell.KeyEnter:
			if c := list.GetCurrentItem(); c >= 0 && c < len(shown) {
				finish(shown[c])
			}
			return nil
		case tcell.KeyEscape:
			finish(-1)
			return nil
		}
		return ev
	})
	w, h := 60, len(items)+3
	for _, it := range items {
		w = max(w, min(len([]rune(it))+6, 120))
	}
	a.modal("pick", box, w, min(max(h, 6), 30))
	a.tv.SetFocus(filter)
}

// showDetail opens the cell detail window (spec 10.1: wrapped, scrollable,
// JSON pretty-printed).
func (a *App) showDetail(v any) {
	prev := a.tv.GetFocus()
	tv := tview.NewTextView().SetWrap(true).SetScrollable(true).SetText(grid.CellDetailText(v))
	tv.SetBorder(true).SetTitle(" Value — Esc closes, c copies ").SetBorderColor(a.theme.BorderFocus)
	tv.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Key() == tcell.KeyEnter:
			a.closeModal("detail", prev)
			return nil
		case ev.Key() == tcell.KeyRune && ev.Rune() == 'c':
			a.copyToClipboard(grid.CellDetailText(v))
			return nil
		}
		return ev
	})
	_, _, w, h := a.pages.GetRect()
	a.modal("detail", tv, max(w*3/4, 40), max(h*3/4, 10))
}

// promptPassword is the Prompter of the workspace; it runs on the worker
// and shows a masked input after the splash (spec 7.1).
func (a *App) promptPassword(prompt string) (string, error) {
	select {
	case <-a.splashDone:
	case <-a.quit:
		return "", script.ErrNoUI
	}
	type result struct {
		pw string
		ok bool
	}
	ch := make(chan result, 1)
	a.tv.QueueUpdateDraw(func() {
		prev := a.tv.GetFocus()
		in := tview.NewInputField().SetLabel(prompt).SetMaskCharacter('*').SetFieldBackgroundColor(tcell.ColorDefault)
		in.SetBorder(true).SetTitle(" Password ").SetBorderColor(a.theme.BorderFocus)
		in.SetDoneFunc(func(k tcell.Key) {
			a.closeModal("password", prev)
			ch <- result{in.GetText(), k == tcell.KeyEnter}
		})
		a.modal("password", in, 60, 3)
	})
	select {
	case r := <-ch:
		if !r.ok {
			return "", fmt.Errorf("password prompt cancelled")
		}
		return r.pw, nil
	case <-a.quit:
		return "", script.ErrNoUI
	}
}

// ---------------------------------------------------------------- help

// helpEntry is one line item of the help overlay.
type helpEntry struct {
	section string
	key     string // key or command usage
	text    string
}

// helpEntries collects the help of the focused pane, generated from the
// live keymap and the command registry (spec 14).
func (a *App) helpEntries(pane string) []helpEntry {
	var out []helpEntry
	title := func(p string) string {
		switch p {
		case PaneGlobal:
			return "Global keys"
		case PaneCmd:
			return "Command line"
		case PaneGrid:
			return "Result grid"
		case PaneTree:
			return "Object tree"
		}
		return strings.ToUpper(p[:1]) + p[1:]
	}
	for _, b := range a.keymap.Bindings(pane) {
		out = append(out, helpEntry{title(pane), b.Key.String(), b.Help})
	}
	if pane == PaneEditor && a.editors.Current() != nil {
		for _, k := range a.editors.Current().Editor.Bindings() {
			out = append(out, helpEntry{"Editing", k.Key, k.Help})
		}
	}
	if pane != PaneGlobal {
		for _, b := range a.keymap.Bindings(PaneGlobal) {
			out = append(out, helpEntry{title(PaneGlobal), b.Key.String(), b.Help})
		}
	}
	out = append(out, helpEntry{title(PaneGlobal), "Esc 1…0", "Esc followed by a digit, or Alt+digit, acts as F1–F10"})
	if a.reg != nil {
		for _, c := range a.reg.Commands() {
			out = append(out, helpEntry{"Commands — help <command> in the command line", c.Usage, c.Help})
		}
	}
	return out
}

// renderHelp formats entries for a width: section headings, keys in a
// fixed column, descriptions wrapped with a hanging indent. Commands put
// the usage on its own line when it is wider than the key column.
func renderHelp(entries []helpEntry, query string, width int) string {
	// The key column fits the longest key of each section (capped), so
	// descriptions line up within a section.
	colOf := map[string]int{}
	for _, e := range entries {
		n := len([]rune(e.key)) + 2
		if n > colOf[e.section] {
			colOf[e.section] = n
		}
	}
	for k, v := range colOf {
		colOf[k] = max(min(v, min(36, width/2)), 12)
	}
	q := strings.ToLower(query)
	var b strings.Builder
	section := ""
	for _, e := range entries {
		if q != "" && !strings.Contains(strings.ToLower(e.key+" "+e.text+" "+e.section), q) {
			continue
		}
		if e.section != section {
			if section != "" {
				b.WriteString("\n")
			}
			section = e.section
			fmt.Fprintf(&b, "[::b]%s[::-]\n", tview.Escape(section))
		}
		keyCol := colOf[e.section]
		key := tview.Escape(e.key)
		lines := wrapWords(e.text, max(width-keyCol-2, 20))
		if len([]rune(e.key)) > keyCol-1 {
			fmt.Fprintf(&b, "  [yellow]%s[-]\n", key)
			for _, l := range lines {
				fmt.Fprintf(&b, "  %s%s\n", strings.Repeat(" ", keyCol), tview.Escape(l))
			}
			continue
		}
		pad := strings.Repeat(" ", keyCol-len([]rune(e.key)))
		for i, l := range lines {
			if i == 0 {
				fmt.Fprintf(&b, "  [yellow]%s[-]%s%s\n", key, pad, tview.Escape(l))
			} else {
				fmt.Fprintf(&b, "  %s%s\n", strings.Repeat(" ", keyCol), tview.Escape(l))
			}
		}
	}
	if b.Len() == 0 {
		return "[gray]no match[-]"
	}
	return b.String()
}

// wrapWords wraps text at word boundaries to width columns.
func wrapWords(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	cur := ""
	for _, w := range words {
		switch {
		case cur == "":
			cur = w
		case len([]rune(cur))+1+len([]rune(w)) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	return append(lines, cur)
}

// showHelp opens the help overlay for the focused pane: all bindings of the
// pane and the global ones, generated from the live keymap, searchable,
// with the About page (spec 14).
func (a *App) showHelp(pane string) {
	prev := a.tv.GetFocus()
	entries := a.helpEntries(pane)
	_, _, w, h := a.pages.GetRect()
	boxW := min(max(w-10, 50), 110)
	tv := tview.NewTextView().SetWrap(false).SetScrollable(true).SetDynamicColors(true)
	search := tview.NewInputField().SetLabel("Search: ").SetFieldBackgroundColor(tcell.ColorDefault).
		SetPlaceholder("type to filter keys and commands").SetPlaceholderTextColor(tcell.ColorGray)
	render := func(q string) {
		tv.SetText(renderHelp(entries, q, boxW-4))
		tv.ScrollToBeginning()
	}
	render("")
	search.SetChangedFunc(render)
	hint := tview.NewTextView().SetDynamicColors(true).
		SetText("[yellow]Esc[-] close   [yellow]F1[-]/[yellow]Tab[-] About page   [yellow]↑ ↓ PgUp PgDn[-] scroll")
	spacer := tview.NewBox()
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(search, 1, 0, true).AddItem(spacer, 1, 0, false).
		AddItem(tv, 0, 1, false).AddItem(hint, 1, 0, false)
	box.SetBorder(true).SetTitle(" Help ").SetBorderColor(a.theme.BorderFocus).SetBorderPadding(0, 0, 1, 1)
	search.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			a.closeModal("help", prev)
			return nil
		case tcell.KeyTab, tcell.KeyF1:
			a.closeModal("help", prev)
			a.showAbout()
			return nil
		case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
			tv.InputHandler()(ev, nil)
			return nil
		}
		return ev
	})
	a.modal("help", box, boxW, max(h-4, 12))
	a.tv.SetFocus(search)
}

// showAbout shows the About page: the splash content without a timer.
func (a *App) showAbout() {
	prev := a.tv.GetFocus()
	var sp *splash.Splash
	sp = splash.New(splash.ModeAbout, func() { a.closeModal("about", prev) })
	a.pages.AddPage("about", sp, true, true)
	a.modalStack = append(a.modalStack, "about")
	a.tv.SetFocus(sp)
}
