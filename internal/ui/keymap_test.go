package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestParseKey(t *testing.T) {
	cases := map[string]Key{
		"f3":         {Code: tcell.KeyF3},
		"F10":        {Code: tcell.KeyF10},
		"ctrl+o":     {Code: tcell.KeyCtrlO},
		"ctrl+space": {Code: tcell.KeyCtrlSpace},
		"alt+,":      {Code: tcell.KeyRune, Rune: ',', Mod: tcell.ModAlt},
		"alt+n":      {Code: tcell.KeyRune, Rune: 'n', Mod: tcell.ModAlt},
		"s":          {Code: tcell.KeyRune, Rune: 's'},
		"S":          {Code: tcell.KeyRune, Rune: 'S'},
		"/":          {Code: tcell.KeyRune, Rune: '/'},
		"enter":      {Code: tcell.KeyEnter},
		"backtab":    {Code: tcell.KeyBacktab},
	}
	for in, want := range cases {
		got, err := ParseKey(in)
		if err != nil || got != want {
			t.Errorf("ParseKey(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "f99", "hyper+x", "abc"} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q): want error", bad)
		}
	}
}

func TestActionLookupAndOverride(t *testing.T) {
	k := DefaultKeymap()
	ev := func(key tcell.Key, r rune, m tcell.ModMask) *tcell.EventKey { return tcell.NewEventKey(key, r, m) }
	if a := k.Action(PaneGrid, ev(tcell.KeyF3, 0, 0)); a != "pivot" {
		t.Errorf("grid F3 = %q", a)
	}
	if a := k.Action(PaneGrid, ev(tcell.KeyF10, 0, 0)); a != "quit" {
		t.Errorf("global fallback F10 = %q", a)
	}
	if a := k.Action(PaneEditor, ev(tcell.KeyRune, ',', tcell.ModAlt)); a != "prev_tab" {
		t.Errorf("alt+, = %q", a)
	}
	if a := k.Action(PaneGrid, ev(tcell.KeyRune, 'S', tcell.ModShift)); a != "sort_add" {
		t.Errorf("S = %q", a)
	}
	if a := k.Action(PaneEditor, ev(tcell.KeyCtrlSpace, 0, tcell.ModCtrl)); a != "complete" {
		t.Errorf("ctrl+space = %q", a)
	}
	if a := k.Action(PaneCmd, ev(tcell.KeyRune, ' ', tcell.ModCtrl)); a != "complete" {
		t.Errorf("CSI-u ctrl+space = %q", a)
	}
	if a := k.Action(PaneGrid, ev(tcell.KeyRune, 'c', tcell.ModCtrl)); a == "copy_cell" {
		t.Errorf("CSI-u ctrl+c must not act as plain c")
	}
	if a := k.Action(PaneForm, ev(tcell.KeyRune, 'n', tcell.ModCtrl)); a != "set_null" {
		t.Errorf("CSI-u ctrl+n = %q", a)
	}
	if err := k.Apply(map[string]map[string]string{"grid": {"pivot": "f6"}}, "ctrl+o"); err != nil {
		t.Fatal(err)
	}
	if a := k.Action(PaneGrid, ev(tcell.KeyF6, 0, 0)); a != "pivot" {
		t.Errorf("override: F6 = %q", a)
	}
	if a := k.Action(PaneCmd, ev(tcell.KeyCtrlO, 0, tcell.ModCtrl)); a != "complete" {
		t.Errorf("alt completion key = %q", a)
	}
	if err := k.Apply(map[string]map[string]string{"grid": {"nope": "f6"}}, ""); err == nil {
		t.Error("unknown action accepted")
	}
}

func TestFooterAndHelp(t *testing.T) {
	k := DefaultKeymap()
	f := k.Footer(PaneEditor)
	if f[0] != "Help" || f[4] != "Run" || f[5] != "RunAll" || f[9] != "Quit" {
		t.Errorf("editor footer %q", f)
	}
	if g := k.Footer(PaneGrid); g[2] != "Pivot" {
		t.Errorf("grid footer %q", g)
	}
	h := k.HelpText(PaneGrid)
	for _, want := range []string{"Toggle pivot view", "Quit", "Esc+digit"} {
		if !strings.Contains(h, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

func TestFKeyTranslator(t *testing.T) {
	now := time.Unix(0, 0)
	tr := &FKeyTranslator{Now: func() time.Time { return now }}
	d := func(r rune, m tcell.ModMask) *tcell.EventKey { return tcell.NewEventKey(tcell.KeyRune, r, m) }
	if e := tr.Translate(d('3', tcell.ModAlt)); e.Key() != tcell.KeyF3 {
		t.Error("alt+3")
	}
	if e := tr.Translate(tcell.NewEventKey(tcell.KeyEscape, 0, 0)); e.Key() != tcell.KeyEscape {
		t.Error("esc delivered")
	}
	now = now.Add(300 * time.Millisecond)
	if e := tr.Translate(d('0', 0)); e.Key() != tcell.KeyF10 {
		t.Error("esc 0 = F10")
	}
	if e := tr.Translate(d('5', 0)); e.Key() != tcell.KeyRune {
		t.Error("plain digit must stay a digit")
	}
	tr.Translate(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
	now = now.Add(2 * time.Second)
	if e := tr.Translate(d('5', 0)); e.Key() != tcell.KeyRune {
		t.Error("digit after timeout must stay a digit")
	}
}
