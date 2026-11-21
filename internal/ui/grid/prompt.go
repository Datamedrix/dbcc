package grid

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Prompt sizes passed to Options.OpenPrompt (outer size including the
// frame the application draws around the prompt).
const (
	promptWidth = 64
)

type promptState struct{}

// showPrompt opens p through Options.OpenPrompt.
func (g *Grid) showPrompt(p tview.Primitive, title string, w, h int) bool {
	if g.opts.OpenPrompt == nil {
		return false
	}
	ps := &promptState{}
	g.prompt = ps
	g.opts.OpenPrompt(p, title, w, h, func() {
		if g.prompt == ps {
			g.prompt = nil
		}
	})
	return true
}

// closePrompt closes the open prompt (if the application has not already).
func (g *Grid) closePrompt() {
	if g.prompt == nil {
		return
	}
	g.prompt = nil
	if g.opts.ClosePrompt != nil {
		g.opts.ClosePrompt()
	}
}

// PromptOpen reports whether a grid prompt is shown.
func (g *Grid) PromptOpen() bool { return g.prompt != nil }

func newInput(label, text string) *tview.InputField {
	return tview.NewInputField().SetLabel(label).SetText(text).SetFieldWidth(0)
}

// SearchPrompt returns the quick-search prompt: an input field with the
// current search text; Enter applies (empty removes the search), Esc
// cancels. done is called after either.
func (g *Grid) SearchPrompt(done func()) tview.Primitive {
	in := newInput("Search: ", g.state.Search)
	in.SetDoneFunc(func(k tcell.Key) {
		switch k {
		case tcell.KeyEnter:
			done()
			g.SetSearch(in.GetText())
		case tcell.KeyEscape:
			done()
		}
	})
	return in
}

func (g *Grid) openSearch() bool {
	if g.content.src == nil || g.opts.OpenPrompt == nil {
		return g.opts.OpenPrompt != nil
	}
	return g.showPrompt(g.SearchPrompt(g.closePrompt), "Quick search", promptWidth, 3)
}

// WherePrompt returns the free-WHERE prompt (table-origin results): an
// input field with the current expression and Options.CompleteWhere as
// autocompletion; Enter applies (empty removes it), Esc cancels.
func (g *Grid) WherePrompt(done func()) tview.Primitive {
	in := newInput("WHERE ", g.state.Where)
	if g.opts.CompleteWhere != nil {
		in.SetAutocompleteFunc(g.opts.CompleteWhere)
	}
	in.SetDoneFunc(func(k tcell.Key) {
		switch k {
		case tcell.KeyEnter:
			done()
			g.SetWhere(in.GetText())
		case tcell.KeyEscape:
			done()
		}
	})
	return in
}

func (g *Grid) openWhere() bool {
	if g.content.src == nil || g.opts.OpenPrompt == nil {
		return g.opts.OpenPrompt != nil
	}
	return g.showPrompt(g.WherePrompt(g.closePrompt), "Filter: WHERE", 100, 3)
}

// ColumnFilterPrompt returns the column-filter prompt for column col: a
// list of operators (FilterOps; digits 1–9, 0 select directly), then —
// for operators with a value — an input field. Enter applies, Esc cancels.
func (g *Grid) ColumnFilterPrompt(col int, done func()) tview.Primitive {
	name := g.colName(col)
	p := newStaged()
	list := tview.NewList().ShowSecondaryText(false)
	list.SetDoneFunc(done)
	for i, op := range FilterOps {
		op := op
		sc := rune('1' + i)
		if i == 9 {
			sc = '0'
		}
		list.AddItem(name+" "+op.String(), "", sc, func() {
			if !op.NeedsValue() {
				done()
				g.AddFilter(ColumnFilter{Col: col, Op: op})
				return
			}
			init := ""
			if op != OpContains && op != OpStartsWith {
				if v, ok := g.CurrentValue(); ok && v != nil {
					if s, isStr := v.(string); isStr {
						init = s
					}
				}
			}
			in := newInput(name+" "+op.String()+" ", init)
			in.SetDoneFunc(func(k tcell.Key) {
				switch k {
				case tcell.KeyEnter:
					done()
					g.AddFilter(ColumnFilter{Col: col, Op: op, Value: in.GetText()})
				case tcell.KeyEscape:
					done()
				}
			})
			p.switchTo(in)
		})
	}
	p.switchTo(list)
	return p
}

func (g *Grid) openColumnFilter() bool {
	if g.content.src == nil || len(g.content.cols) == 0 || g.opts.OpenPrompt == nil {
		return g.opts.OpenPrompt != nil
	}
	col := g.CurrentColumn()
	return g.showPrompt(g.ColumnFilterPrompt(col, g.closePrompt), "Filter: "+g.colName(col), promptWidth, len(FilterOps)+2)
}

// RemoveMenu returns the "x" menu: one entry per ActiveItems item plus
// "all"; Enter removes the chosen one, Esc cancels.
func (g *Grid) RemoveMenu(done func()) tview.Primitive {
	items := g.ActiveItems()
	list := tview.NewList().ShowSecondaryText(false)
	list.SetDoneFunc(done)
	for i, it := range items {
		i := i
		sc := rune(0)
		if i < 9 {
			sc = rune('1' + i)
		}
		list.AddItem(it, "", sc, func() { done(); g.Remove(i) })
	}
	list.AddItem("all", "", 'a', func() { done(); g.RemoveAll() })
	return list
}

func (g *Grid) openRemoveMenu(items []string) bool {
	if g.opts.OpenPrompt == nil {
		return false
	}
	w := 20
	for _, it := range items {
		w = max(w, len(it)+10)
	}
	return g.showPrompt(g.RemoveMenu(g.closePrompt), "Remove", min(w, 100), len(items)+3)
}

// staged is a primitive that shows one child at a time and moves the
// focus when the child changes (operator list → value input).
type staged struct {
	*tview.Box
	active   tview.Primitive
	setFocus func(tview.Primitive)
}

func newStaged() *staged { return &staged{Box: tview.NewBox()} }

func (p *staged) switchTo(c tview.Primitive) {
	p.active = c
	if p.setFocus != nil {
		p.setFocus(c)
	}
}

func (p *staged) Draw(screen tcell.Screen) {
	p.Box.DrawForSubclass(screen, p)
	x, y, w, h := p.GetInnerRect()
	p.active.SetRect(x, y, w, h)
	p.active.Draw(screen)
}

func (p *staged) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return p.WrapInputHandler(func(ev *tcell.EventKey, setFocus func(tview.Primitive)) {
		p.setFocus = setFocus
		if h := p.active.InputHandler(); h != nil {
			h(ev, setFocus)
		}
	})
}

func (p *staged) PasteHandler() func(string, func(tview.Primitive)) {
	return p.WrapPasteHandler(func(text string, setFocus func(tview.Primitive)) {
		if h := p.active.PasteHandler(); h != nil {
			h(text, setFocus)
		}
	})
}

func (p *staged) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return p.active.MouseHandler()
}

func (p *staged) Focus(delegate func(tview.Primitive)) {
	p.setFocus = delegate
	delegate(p.active)
}

func (p *staged) HasFocus() bool { return p.active.HasFocus() }
