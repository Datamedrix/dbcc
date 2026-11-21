package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
)

// Pane names used in the keymap and in [keys.<pane>] configuration.
const (
	PaneGlobal = "global"
	PaneTree   = "tree"
	PaneEditor = "editor"
	PaneGrid   = "grid"
	PaneForm   = "form"
	PaneCmd    = "cmdline"
)

// Key is a normalised key: a tcell key code, a rune and modifiers.
type Key struct {
	Code tcell.Key
	Rune rune
	Mod  tcell.ModMask
}

// Binding is one action of a pane.
type Binding struct {
	Action string // stable name, used in config: [keys.grid] pivot = "f3"
	Key    Key
	Label  string // short footer label ("Run"); empty = not in footer
	Help   string // help overlay description
}

// Keymap holds the bindings of all panes.
type Keymap struct {
	panes map[string][]Binding
}

// DefaultKeymap returns the defaults of spec section 14.
func DefaultKeymap() *Keymap {
	k := &Keymap{panes: map[string][]Binding{}}
	add := func(pane, action, key, label, help string) {
		kk, err := ParseKey(key)
		if err != nil {
			panic(err)
		}
		k.panes[pane] = append(k.panes[pane], Binding{Action: action, Key: kk, Label: label, Help: help})
	}
	g := PaneGlobal
	add(g, "help", "f1", "Help", "Help overlay for the focused pane")
	add(g, "profiles", "f2", "Conn", "Profiles: open, switch, close connections")
	add(g, "cancel", "f8", "Cancel", "Cancel the running statement")
	add(g, "cmdline", "f9", "Cmd", "Focus the command line")
	add(g, "quit", "f10", "Quit", "Quit")
	add(g, "focus_tree", "alt+f1", "", "Go to the object tree")
	add(g, "focus_editor", "alt+f2", "", "Go to the editor (scratchpad)")
	add(g, "focus_results", "alt+f3", "", "Go to the results")
	add(g, "next_pane", "tab", "", "Next pane")
	add(g, "prev_pane", "backtab", "", "Previous pane")

	t := PaneTree
	add(t, "open", "enter", "", "Default action: expand, open data, or open source")
	add(t, "cat", "f3", "View", "cat the selected object")
	add(t, "edit", "f4", "Edit", "edit: source of a view or routine in an editor tab")
	add(t, "reload", "f5", "Reload", "Reload the selected node")
	add(t, "new_row", "f7", "New", "New row for the selected table")
	add(t, "filter", "/", "", "Filter the tree by name")
	add(t, "chdir", "c", "", "Set the current path to the selected node")

	e := PaneEditor
	add(e, "run_statement", "f5", "Run", "Run statement at cursor")
	add(e, "run_all", "f6", "RunAll", "Run selection, or the whole buffer of the current tab")
	add(e, "complete", "ctrl+space", "", "Completion list")
	add(e, "prev_tab", "alt+,", "", "Previous editor tab")
	add(e, "next_tab", "alt+.", "", "Next editor tab")
	add(e, "new_tab", "alt+n", "", "New scratch tab")
	add(e, "close_tab", "alt+x", "", "Close the tab (asks if there are unsaved changes)")
	add(e, "restore", "alt+r", "", "Restore the source as loaded (object tabs)")

	r := PaneGrid
	add(r, "pivot", "f3", "Pivot", "Toggle pivot view")
	add(r, "edit_row", "f4", "Edit", "Edit the record under the cursor")
	add(r, "rerun", "f5", "Rerun", "Re-run the query")
	add(r, "new_row", "f7", "New", "New row")
	add(r, "detail", "enter", "", "Cell detail")
	add(r, "sort", "s", "", "Sort by current column (cycle)")
	add(r, "sort_add", "S", "", "Add current column as further sort key")
	add(r, "search", "/", "", "Quick search")
	add(r, "filter_column", "f", "", "Column filter on the current column")
	add(r, "filter_where", "w", "", "Free WHERE (table-origin only)")
	add(r, "clear_filters", "x", "", "Remove filters and sorting")
	add(r, "load_all", "a", "", "Load all rows up to the cap")
	add(r, "copy_cell", "c", "", "Copy cell")
	add(r, "copy_row", "C", "", "Copy row")
	add(r, "prev_result", "[", "", "Previous result tab")
	add(r, "next_result", "]", "", "Next result tab")
	add(r, "close_result", "alt+x", "", "Close the result tab (Messages stays)")
	add(r, "close_all_results", "alt+X", "", "Close all result tabs")

	f := PaneForm
	add(f, "save", "f2", "Save", "Save")
	add(f, "lookup", "f4", "Lookup", "Open lookup or multi-line editor for the current field")
	add(f, "save_new", "f6", "Save+New", "Save and new")
	add(f, "set_null", "ctrl+n", "", "Set NULL")
	add(f, "untouch", "ctrl+u", "", "Reset field to untouched")
	add(f, "cancel", "esc", "", "Cancel")

	c := PaneCmd
	add(c, "complete", "ctrl+space", "", "Complete command or argument")
	add(c, "execute", "enter", "", "Run the command or SQL")
	return k
}

// Apply overrides bindings from configuration ([keys.<pane>] action = "key")
// and sets the alternative completion key. Unknown actions are errors.
func (k *Keymap) Apply(cfg map[string]map[string]string, completionAlt string) error {
	for pane, m := range cfg {
		bs, ok := k.panes[pane]
		if !ok {
			return fmt.Errorf("keys.%s: unknown pane", pane)
		}
		for action, key := range m {
			kk, err := ParseKey(key)
			if err != nil {
				return fmt.Errorf("keys.%s.%s: %w", pane, action, err)
			}
			found := false
			for i := range bs {
				if bs[i].Action == action {
					bs[i].Key, found = kk, true
				}
			}
			if !found {
				return fmt.Errorf("keys.%s: unknown action %q", pane, action)
			}
		}
	}
	if completionAlt != "" {
		kk, err := ParseKey(completionAlt)
		if err != nil {
			return fmt.Errorf("completion_key_alt: %w", err)
		}
		for _, pane := range []string{PaneEditor, PaneCmd} {
			k.panes[pane] = append(k.panes[pane], Binding{Action: "complete", Key: kk, Help: "Completion list (alternative key)"})
		}
	}
	return nil
}

// Bindings returns the bindings of a pane in definition order.
func (k *Keymap) Bindings(pane string) []Binding { return k.panes[pane] }

// Action returns the action bound to ev in pane, falling back to global.
func (k *Keymap) Action(pane string, ev *tcell.EventKey) string {
	key := FromEvent(ev)
	for _, p := range []string{pane, PaneGlobal} {
		for _, b := range k.panes[p] {
			if b.Key == key {
				return b.Action
			}
		}
	}
	return ""
}

// Footer returns the F-key labels for the focused pane: "1Help 2Conn …"
// as (number, label) pairs for F1–F10. Pane bindings shadow global ones.
func (k *Keymap) Footer(pane string) [10]string {
	var out [10]string
	for _, p := range []string{PaneGlobal, pane} {
		for _, b := range k.panes[p] {
			if b.Key.Code >= tcell.KeyF1 && b.Key.Code <= tcell.KeyF10 && b.Key.Mod == 0 {
				i := int(b.Key.Code - tcell.KeyF1)
				if b.Label != "" || p == pane {
					out[i] = b.Label
				}
			}
		}
	}
	return out
}

// HelpText renders all bindings of the pane and the global ones.
func (k *Keymap) HelpText(pane string) string {
	var b strings.Builder
	for _, p := range []string{pane, PaneGlobal} {
		bs := k.panes[p]
		if len(bs) == 0 || (p == PaneGlobal && pane == PaneGlobal && b.Len() > 0) {
			continue
		}
		fmt.Fprintf(&b, "%s\n", strings.ToUpper(p[:1])+p[1:])
		for _, x := range bs {
			fmt.Fprintf(&b, "  %-12s %s\n", x.Key.String(), x.Help)
		}
		b.WriteString("\n")
	}
	b.WriteString("Esc+digit and Alt+digit act as F1–F10.\n")
	return b.String()
}

var keyNames = map[string]tcell.Key{
	"enter": tcell.KeyEnter, "tab": tcell.KeyTab, "backtab": tcell.KeyBacktab,
	"esc": tcell.KeyEscape, "escape": tcell.KeyEscape, "backspace": tcell.KeyBackspace2,
	"delete": tcell.KeyDelete, "insert": tcell.KeyInsert, "home": tcell.KeyHome, "end": tcell.KeyEnd,
	"pgup": tcell.KeyPgUp, "pgdn": tcell.KeyPgDn, "up": tcell.KeyUp, "down": tcell.KeyDown,
	"left": tcell.KeyLeft, "right": tcell.KeyRight,
}

// ParseKey parses "f3", "ctrl+o", "alt+,", "ctrl+space", "s", "S", "/".
func ParseKey(s string) (Key, error) {
	orig := s
	if s == "" {
		return Key{}, fmt.Errorf("empty key")
	}
	var mod tcell.ModMask
	low := strings.ToLower(s)
	for {
		switch {
		case strings.HasPrefix(low, "ctrl+") && len(low) > 5:
			mod |= tcell.ModCtrl
			s, low = s[5:], low[5:]
			continue
		case strings.HasPrefix(low, "alt+") && len(low) > 4:
			mod |= tcell.ModAlt
			s, low = s[4:], low[4:]
			continue
		case strings.HasPrefix(low, "shift+") && len(low) > 6:
			mod |= tcell.ModShift
			s, low = s[6:], low[6:]
			continue
		}
		break
	}
	if len(low) >= 2 && low[0] == 'f' {
		var n int
		if _, err := fmt.Sscanf(low[1:], "%d", &n); err == nil && n >= 1 && n <= 24 && fmt.Sprint(n) == low[1:] {
			return Key{Code: tcell.KeyF1 + tcell.Key(n-1), Mod: mod}, nil
		}
	}
	if c, ok := keyNames[low]; ok {
		return Key{Code: c, Mod: mod}, nil
	}
	if mod&tcell.ModCtrl != 0 && mod&^tcell.ModCtrl == 0 {
		if low == "space" {
			return Key{Code: tcell.KeyCtrlSpace}, nil
		}
		r := []rune(low)
		if len(r) == 1 && r[0] >= 'a' && r[0] <= 'z' {
			return Key{Code: tcell.KeyCtrlA + tcell.Key(r[0]-'a')}, nil
		}
	}
	r := []rune(s)
	if low == "space" {
		r = []rune{' '}
	}
	if len(r) == 1 {
		return Key{Code: tcell.KeyRune, Rune: r[0], Mod: mod}, nil
	}
	return Key{}, fmt.Errorf("unknown key %q", orig)
}

// FromEvent normalises a tcell event to a Key.
func FromEvent(ev *tcell.EventKey) Key {
	if ev.Key() == tcell.KeyRune {
		r, mods := ev.Rune(), ev.Modifiers()
		// CSI-u terminals (kitty, …) report Ctrl+letter as a rune with
		// ModCtrl; normalise to the legacy key codes used by the keymap.
		if mods&tcell.ModCtrl != 0 {
			switch {
			case r == ' ':
				return Key{Code: tcell.KeyCtrlSpace, Mod: mods & tcell.ModAlt}
			case unicode.IsLetter(r) && r < 128:
				return Key{Code: tcell.KeyCtrlA + tcell.Key(unicode.ToLower(r)-'a'), Mod: mods & tcell.ModAlt}
			}
		}
		return Key{Code: tcell.KeyRune, Rune: r, Mod: mods & tcell.ModAlt} // shift is part of the rune
	}
	code := ev.Key()
	mod := ev.Modifiers()
	// In tcell v2.13 KeyCtrlA…KeyCtrlZ and KeyCtrlSpace are distinct codes
	// (Ctrl+H/I/M still arrive as Backspace/Tab/Enter); ModCtrl is implied.
	if code >= tcell.KeyCtrlA && code <= tcell.KeyCtrlZ || code == tcell.KeyCtrlSpace {
		mod &^= tcell.ModCtrl
	}
	if code == tcell.KeyBacktab {
		mod &^= tcell.ModShift
	}
	return Key{Code: code, Mod: mod &^ tcell.ModMeta}
}

// String renders a key for the help overlay.
func (k Key) String() string {
	var parts []string
	if k.Mod&tcell.ModCtrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if k.Mod&tcell.ModAlt != 0 {
		parts = append(parts, "Alt")
	}
	if k.Mod&tcell.ModShift != 0 {
		parts = append(parts, "Shift")
	}
	var name string
	switch {
	case k.Code == tcell.KeyRune && k.Rune == ' ':
		name = "Space"
	case k.Code == tcell.KeyRune:
		name = string(k.Rune)
	case k.Code >= tcell.KeyF1 && k.Code <= tcell.KeyF64:
		name = fmt.Sprintf("F%d", k.Code-tcell.KeyF1+1)
	case k.Code == tcell.KeyCtrlSpace:
		name = "Ctrl+Space"
	case k.Code >= tcell.KeyCtrlA && k.Code <= tcell.KeyCtrlZ:
		name = "Ctrl+" + string(rune('A'+k.Code-tcell.KeyCtrlA))
	case k.Code == tcell.KeyBacktab:
		name = "Shift+Tab"
	default:
		name = tcell.KeyNames[k.Code]
		if name == "" {
			name = fmt.Sprintf("key%d", k.Code)
		}
	}
	return strings.Join(append(parts, name), "+")
}

// FKeyTranslator implements the Midnight Commander fallback (spec 5.2):
// Esc followed by a digit, and Alt+digit, act as F1–F10 ("0" = F10).
// Esc itself is delivered immediately (it only closes overlays and clears
// filters); a digit that follows within Timeout becomes the F-key. tcell
// already reports a fast Esc+digit sequence as Alt+digit.
type FKeyTranslator struct {
	Timeout time.Duration
	Now     func() time.Time // for tests
	escAt   time.Time
}

// Translate returns the event to deliver, possibly a synthesised F-key.
func (t *FKeyTranslator) Translate(ev *tcell.EventKey) *tcell.EventKey {
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	timeout := t.Timeout
	if timeout == 0 {
		timeout = time.Second
	}
	escAt := t.escAt
	t.escAt = time.Time{}
	if ev.Key() == tcell.KeyEscape && ev.Modifiers() == 0 {
		t.escAt = now()
		return ev
	}
	if ev.Key() == tcell.KeyRune && ev.Rune() >= '0' && ev.Rune() <= '9' {
		alt := ev.Modifiers()&tcell.ModAlt != 0
		afterEsc := !escAt.IsZero() && now().Sub(escAt) <= timeout
		if alt || afterEsc {
			n := int(ev.Rune() - '0')
			if n == 0 {
				n = 10
			}
			return tcell.NewEventKey(tcell.KeyF1+tcell.Key(n-1), 0, tcell.ModNone)
		}
	}
	return ev
}

// sortedPanes lists configured panes deterministically (for tests).
func (k *Keymap) sortedPanes() []string {
	var out []string
	for p := range k.panes {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
