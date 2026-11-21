package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// onUI runs f on the UI goroutine and waits for it.
func onUI(t *testing.T, a *App, f func()) {
	t.Helper()
	done := make(chan struct{})
	a.tv.QueueUpdate(func() { f(); close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("UI goroutine did not respond")
	}
}

// eventually polls cond on the UI goroutine.
func eventually(t *testing.T, a *App, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		onUI(t, a, func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func cmdText(t *testing.T, a *App) string {
	var s string
	onUI(t, a, func() { s = a.cmd.GetText() })
	return s
}

// startCmdline starts the app, waits for the command registry and
// focuses the command line (F9).
func startCmdline(t *testing.T) (*App, tcell.SimulationScreen, func()) {
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	eventually(t, a, "command registry", func() bool { return a.reg != nil })
	scr.InjectKey(tcell.KeyF9, 0, tcell.ModNone)
	eventually(t, a, "command line focus", func() bool { return a.cmd.HasFocus() })
	return a, scr, stop
}

func typeRunes(scr tcell.SimulationScreen, s string) {
	for _, r := range s {
		scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

// openCList types prefix, presses Ctrl+Space and returns the candidates.
func openCList(t *testing.T, a *App, scr tcell.SimulationScreen, prefix string) []string {
	t.Helper()
	typeRunes(scr, prefix)
	eventually(t, a, "typed prefix", func() bool { return a.cmd.GetText() == prefix })
	var cands []string
	onUI(t, a, func() { cands = a.reg.Complete(prefix) })
	if len(cands) < 3 {
		t.Fatalf("need >= 3 candidates for %q, got %v", prefix, cands)
	}
	scr.InjectKey(tcell.KeyCtrlSpace, 0, tcell.ModCtrl)
	eventually(t, a, "completion list open", func() bool { return a.cmdList })
	screen := strings.Join(waitFor(t, a, scr, cands[2], 2*time.Second), "\n")
	for _, c := range cands[:3] {
		if !strings.Contains(screen, c) {
			t.Errorf("candidate %q not shown", c)
		}
	}
	return cands
}

func TestCmdlineCompletionListNavigateEnter(t *testing.T) {
	a, scr, stop := startCmdline(t)
	defer stop()
	cands := openCList(t, a, scr, "c")
	scr.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	scr.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	eventually(t, a, "list closed", func() bool { return !a.cmdList })
	if got := cmdText(t, a); got != cands[2] {
		t.Fatalf("command line = %q, want third candidate %q", got, cands[2])
	}
	// Enter accepted the entry only; nothing ran, history untouched.
	var hist int
	var focus bool
	onUI(t, a, func() { hist = len(a.cmdHistory); focus = a.cmd.HasFocus() })
	if hist != 0 || !focus {
		t.Errorf("history=%d focus=%v, want 0/true", hist, focus)
	}
	// Typing after accepting does not reopen the list.
	typeRunes(scr, " ")
	eventually(t, a, "space typed", func() bool { return strings.HasSuffix(a.cmd.GetText(), " ") })
	var open bool
	onUI(t, a, func() { open = a.cmdList })
	if open {
		t.Error("list reopened by typing after it was closed")
	}
}

func TestCmdlineCompletionTabAcceptsNotNextPane(t *testing.T) {
	a, scr, stop := startCmdline(t)
	defer stop()
	cands := openCList(t, a, scr, "c")
	scr.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	scr.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	eventually(t, a, "list closed", func() bool { return !a.cmdList })
	var focus bool
	onUI(t, a, func() { focus = a.cmd.HasFocus() })
	if !focus {
		t.Fatal("Tab switched the pane instead of accepting the completion")
	}
	if got := cmdText(t, a); got != cands[1] {
		t.Fatalf("command line = %q, want %q", got, cands[1])
	}
}

func TestCmdlineCompletionEscClosesOnlyList(t *testing.T) {
	a, scr, stop := startCmdline(t)
	defer stop()
	openCList(t, a, scr, "c")
	scr.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	eventually(t, a, "list closed", func() bool { return !a.cmdList })
	var focus bool
	onUI(t, a, func() { focus = a.cmd.HasFocus() })
	if !focus {
		t.Fatal("Esc with the list open left the command line")
	}
	if got := cmdText(t, a); got != "c" {
		t.Fatalf("command line = %q, want %q (kept)", got, "c")
	}
	// Typing now must not reopen the list (never automatically).
	typeRunes(scr, "d")
	eventually(t, a, "typed", func() bool { return a.cmd.GetText() == "cd" })
	var open bool
	onUI(t, a, func() { open = a.cmdList })
	if open {
		t.Error("list reopened after Esc")
	}
	// A second Esc (no list) clears the line and leaves the pane as before.
	scr.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	eventually(t, a, "focus left", func() bool { return !a.cmd.HasFocus() && a.cmd.GetText() == "" })
}

func TestCmdlineCompletionFiltersWhileOpen(t *testing.T) {
	a, scr, stop := startCmdline(t)
	defer stop()
	openCList(t, a, scr, "c")
	typeRunes(scr, "a")
	eventually(t, a, "typed", func() bool { return a.cmd.GetText() == "ca" })
	var want []string
	onUI(t, a, func() { want = a.reg.Complete("ca") })
	var open bool
	onUI(t, a, func() { open = a.cmdList })
	if open != (len(want) > 0) {
		t.Fatalf("list open=%v after filtering, candidates %v", open, want)
	}
}

func TestCmdlineHistoryWraps(t *testing.T) {
	a, scr, stop := startCmdline(t)
	defer stop()
	for _, c := range []string{"pwd", "help"} {
		typeRunes(scr, c)
		eventually(t, a, "typed "+c, func() bool { return a.cmd.GetText() == c })
		scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
		eventually(t, a, "ran "+c, func() bool { return a.cmd.GetText() == "" })
		// wait for the worker to finish the command (its output may move
		// the focus), then come back to the command line.
		flushed := make(chan struct{})
		a.do(func() { close(flushed) })
		<-flushed
		onUI(t, a, func() {})
		scr.InjectKey(tcell.KeyF9, 0, tcell.ModNone)
		eventually(t, a, "focus", func() bool {
			name, _ := a.pages.GetFrontPage()
			return name == "main" && a.cmd.HasFocus()
		})
	}
	step := func(k tcell.Key, want string) {
		t.Helper()
		scr.InjectKey(k, 0, tcell.ModNone)
		eventually(t, a, "history "+want, func() bool { return a.cmd.GetText() == want })
	}
	step(tcell.KeyUp, "help")
	step(tcell.KeyUp, "pwd")
	step(tcell.KeyUp, "")     // wrap: oldest -> empty line
	step(tcell.KeyUp, "help") // and on to the newest
	step(tcell.KeyDown, "")
	step(tcell.KeyDown, "pwd") // wrap: empty line -> oldest
	step(tcell.KeyDown, "help")
}
