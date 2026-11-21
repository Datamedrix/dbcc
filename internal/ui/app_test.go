package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/config"
)

// screenText returns the simulation screen as lines of text.
func screenText(s tcell.SimulationScreen) []string {
	cells, w, h := s.GetContents()
	lines := make([]string, h)
	for y := 0; y < h; y++ {
		var b strings.Builder
		for x := 0; x < w; x++ {
			c := cells[y*w+x]
			if len(c.Runes) == 0 {
				b.WriteRune(' ')
			} else {
				b.WriteRune(c.Runes[0])
			}
		}
		lines[y] = b.String()
	}
	return lines
}

// waitFor polls until the screen contains want or the timeout expires.
func waitFor(t *testing.T, a *App, s tcell.SimulationScreen, want string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lines []string
	for time.Now().Before(deadline) {
		done := make(chan struct{})
		a.tv.QueueUpdate(func() { lines = screenText(s); close(done) })
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		if strings.Contains(strings.Join(lines, "\n"), want) {
			return lines
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q:\n%s", want, strings.Join(lines, "\n"))
	return nil
}

// startApp runs an App on a simulation screen and returns it with a stop func.
func startApp(t *testing.T, cfg *config.Config, opts Options) (*App, tcell.SimulationScreen, func()) {
	t.Helper()
	scr := tcell.NewSimulationScreen("UTF-8")
	scr.SetSize(120, 36)
	opts.Config = cfg
	opts.Screen = scr
	if opts.StateDir == "" {
		opts.StateDir = t.TempDir()
	}
	if opts.UserCmdDir == "" {
		opts.UserCmdDir = t.TempDir()
	}
	a, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- a.Run(context.Background()) }()
	stop := func() {
		a.tv.QueueUpdate(a.tv.Stop)
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("app did not stop")
		}
	}
	return a, scr, stop
}

func emptyConfig() *config.Config {
	return &config.Config{Settings: config.DefaultSettings(), Profiles: map[string]*config.Profile{}}
}

func TestSplashTimerThenMainLayout(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{SplashDuration: 300 * time.Millisecond})
	defer stop()
	waitFor(t, a, scr, "Database Command & Control", 2*time.Second)
	start := time.Now()
	lines := waitFor(t, a, scr, "1Help", 3*time.Second)
	if time.Since(start) < 150*time.Millisecond {
		t.Errorf("splash vanished too early")
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"no connection", "[scratch ", "Messages", "10Quit", "5Run"} {
		if !strings.Contains(all, want) {
			t.Errorf("main layout lacks %q", want)
		}
	}
}

func TestSplashDismissedByKey(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{SplashDuration: time.Hour})
	defer stop()
	waitFor(t, a, scr, "A product by Datamedrix GmbH", 2*time.Second)
	scr.InjectKey(tcell.KeyRune, 'x', tcell.ModNone)
	waitFor(t, a, scr, "1Help", 2*time.Second)
}

func TestNoSplashWhenDisabled(t *testing.T) {
	cfg := emptyConfig()
	cfg.Settings.Splash = false
	a, scr, stop := startApp(t, cfg, Options{SplashDuration: time.Hour})
	defer stop()
	lines := waitFor(t, a, scr, "1Help", 2*time.Second)
	if strings.Contains(strings.Join(lines, "\n"), "Datamedrix GmbH") {
		t.Error("splash shown although splash = false")
	}
}

func TestFooterFollowsFocusAndHelpOverlay(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second) // editor focused
	scr.InjectKey(tcell.KeyBacktab, 0, tcell.ModNone)
	waitFor(t, a, scr, "5Reload", 2*time.Second) // tree focused
	// Esc then digit acts as an F-key (Midnight Commander fallback): Esc 1 = F1.
	scr.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	scr.InjectKey(tcell.KeyRune, '1', tcell.ModNone)
	waitFor(t, a, scr, "Filter the tree by name", 2*time.Second)
	scr.InjectKey(tcell.KeyTab, 0, tcell.ModNone) // About page
	waitFor(t, a, scr, "Contact: Anton Grünberg", 2*time.Second)
	scr.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	waitFor(t, a, scr, "5Reload", 2*time.Second)
}

func TestEditorTypingRunsWithoutConnection(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	for _, r := range "SELECT 1;" {
		scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	waitFor(t, a, scr, "SELECT 1;", 2*time.Second)
	scr.InjectKey(tcell.KeyF5, 0, tcell.ModNone)
	waitFor(t, a, scr, "no connection: F2 opens the connections", 2*time.Second)
}

func TestWatchdogDumpsHungUI(t *testing.T) {
	state := t.TempDir()
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true, StateDir: state})
	defer stop()
	waitFor(t, a, scr, "1Help", 2*time.Second)
	if name := a.writeDump("test"); name == "" {
		t.Fatal("no dump written")
	} else if data, _ := os.ReadFile(name); !strings.Contains(string(data), "goroutine") {
		t.Fatalf("dump lacks stacks: %.200s", data)
	}
}

// TestWatchdogFiresWhenEventLoopSpins blocks the UI goroutine and expects
// the watchdog to write a dump although tview's update queue fills up.
func TestWatchdogFiresWhenEventLoopSpins(t *testing.T) {
	state := t.TempDir()
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true, StateDir: state})
	waitFor(t, a, scr, "1Help", 2*time.Second)
	release := make(chan struct{})
	go a.tv.QueueUpdate(func() { <-release }) // the event loop hangs here (QueueUpdate is synchronous)
	for i := 0; i < 150; i++ {                // fill tview's update queue (capacity 100)
		go a.tv.QueueUpdate(func() {})
	}
	deadline := time.Now().Add(watchdogInterval + watchdogTimeout + 3*time.Second)
	var found bool
	for time.Now().Before(deadline) && !found {
		matches, _ := filepath.Glob(filepath.Join(state, "hang-*-ui-unresponsive.txt"))
		found = len(matches) > 0
		time.Sleep(100 * time.Millisecond)
	}
	close(release)
	stop()
	if !found {
		t.Fatal("watchdog wrote no dump while the event loop was blocked")
	}
}

func TestQuickNavigationAltF1F2F3(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second) // editor focused
	scr.InjectKey(tcell.KeyF1, 0, tcell.ModAlt)
	waitFor(t, a, scr, "5Reload", 2*time.Second) // tree
	scr.InjectKey(tcell.KeyF3, 0, tcell.ModAlt)
	waitFor(t, a, scr, "3Pivot", 2*time.Second) // results
	scr.InjectKey(tcell.KeyF2, 0, tcell.ModAlt)
	waitFor(t, a, scr, "6RunAll", 2*time.Second) // editor
}

// TestModalIsOpaque: nothing of the pane underneath may show through a
// modal window (Flex does not clear its background; the help overlay's
// padding column showed grid values).
func TestModalIsOpaque(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	// Fill the editor with a marker that would show through the help box.
	a.tv.QueueUpdateDraw(func() {
		a.editors.Current().Editor.SetText(strings.Repeat(strings.Repeat("#", 200)+"\n", 40))
	})
	scr.InjectKey(tcell.KeyF1, 0, tcell.ModNone)
	lines := waitFor(t, a, scr, "Search:", 2*time.Second)
	// The help box's padding columns sit just inside its vertical borders;
	// the right one lies over the editor's '#' text.
	top, right := -1, -1
	for n, l := range lines {
		if i := strings.Index(l, "Search:"); i >= 0 {
			top = n
			r := []rune(l)
			for j := len([]rune(l[:i])); j < len(r); j++ { // first border right of the search field
				if r[j] == '║' || r[j] == '│' {
					right = j
					break
				}
			}
			break
		}
	}
	if top < 0 || right < 1 {
		t.Fatal("help frame not found")
	}
	for n := top; n < len(lines) && n < top+20; n++ {
		r := []rune(lines[n])
		if right < len(r) && r[right-1] == '#' {
			t.Fatalf("line %d: background shows through the modal's padding: %q", n, string(r[right-10:right+1]))
		}
	}
}

func TestScratchAutosaveCloseReopen(t *testing.T) {
	state := t.TempDir()
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true, StateDir: state, AutosaveDelay: 100 * time.Millisecond})
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	for _, r := range "SELECT 42;" {
		scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	waitFor(t, a, scr, "SELECT 42;", 2*time.Second)
	dir := filepath.Join(state, "scratches")
	var file string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && file == "" {
		m, _ := filepath.Glob(filepath.Join(dir, "*", "scratch_*.sql"))
		for _, f := range m {
			if b, _ := os.ReadFile(f); string(b) == "SELECT 42;" {
				file = f
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if file == "" {
		t.Fatal("scratch was not saved automatically")
	}
	// Alt+X closes (already saved); a fresh empty scratch takes its place.
	scr.InjectKey(tcell.KeyRune, 'x', tcell.ModAlt)
	waitFor(t, a, scr, "  1  ", 2*time.Second)
	// Alt+O lists it; Enter re-opens it.
	scr.InjectKey(tcell.KeyRune, 'o', tcell.ModAlt)
	waitFor(t, a, scr, "Scratches (2)", 2*time.Second)
	// Filter by content (digits could also match the timestamp in a name).
	for _, r := range "SELECT 4" {
		scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	waitFor(t, a, scr, "Scratches (2)", 2*time.Second)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	// The dialog is closed and the scratch is the current editor tab.
	deadline = time.Now().Add(2 * time.Second)
	for {
		lines := waitFor(t, a, scr, "SELECT 42;", 2*time.Second)
		if !strings.Contains(strings.Join(lines, "\n"), "Scratches (") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scratch dialog did not close after Enter")
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()

	// Restart: the open scratches come back.
	a2, scr2, stop2 := startApp(t, emptyConfig(), Options{NoSplash: true, StateDir: state})
	defer stop2()
	waitFor(t, a2, scr2, "SELECT 42;", 2*time.Second)
}

func TestCtrlCNeverQuitsAndCopies(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	for _, r := range "SELECT 7" {
		scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	scr.InjectKey(tcell.KeyRune, 'a', tcell.ModAlt) // select all
	scr.InjectKey(tcell.KeyCtrlC, 0, tcell.ModCtrl) // legacy form of Ctrl+Shift+C
	time.Sleep(200 * time.Millisecond)
	clip := make(chan string, 1)
	a.tv.QueueUpdate(func() { clip <- a.editors.Current().Editor.LastKill() })
	if got := <-clip; got != "SELECT 7" {
		t.Fatalf("copy buffer %q", got)
	}
	// Ctrl+C in another pane does not stop the application either.
	scr.InjectKey(tcell.KeyBacktab, 0, tcell.ModNone)
	scr.InjectKey(tcell.KeyCtrlC, 0, tcell.ModCtrl)
	waitFor(t, a, scr, "F10 quits", 2*time.Second)
	// CSI-u form: Ctrl+Shift+V as rune with modifiers pastes into the editor.
	scr.InjectKey(tcell.KeyRune, 'v', tcell.ModNone) // tree pane: no effect on the editor
	scr.InjectKey(tcell.KeyF2, 0, tcell.ModAlt)
	scr.InjectKey(tcell.KeyEnd, 0, tcell.ModCtrl)
	scr.InjectKey(tcell.KeyRune, 'V', tcell.ModCtrl|tcell.ModShift)
	waitFor(t, a, scr, "SELECT 7SELECT 7", 2*time.Second)
}

func TestShiftF6AndF18AreShiftF6(t *testing.T) {
	k := DefaultKeymap()
	if a := k.Action(PaneGrid, tcell.NewEventKey(tcell.KeyF6, 0, tcell.ModShift)); a != "prev_page" {
		t.Errorf("Shift+F6 = %q", a)
	}
	if a := k.Action(PaneGrid, tcell.NewEventKey(tcell.KeyF18, 0, tcell.ModNone)); a != "prev_page" {
		t.Errorf("F18 (legacy Shift+F6) = %q", a)
	}
	if a := k.Action(PaneGrid, tcell.NewEventKey(tcell.KeyF6, 0, tcell.ModNone)); a != "next_page" {
		t.Errorf("F6 = %q", a)
	}
}

func TestCtrlShiftXCutsSelection(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	text := func() string {
		ch := make(chan string, 1)
		a.tv.QueueUpdate(func() { ch <- a.editors.Current().Editor.Text() + "|" + a.editors.Current().Editor.LastKill() })
		return <-ch
	}
	for _, r := range "SELECT 9" {
		scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	scr.InjectKey(tcell.KeyRune, 'a', tcell.ModAlt)                 // select all
	scr.InjectKey(tcell.KeyRune, 'X', tcell.ModCtrl|tcell.ModShift) // CSI-u Ctrl+Shift+X
	time.Sleep(200 * time.Millisecond)
	if got := text(); got != "|SELECT 9" {
		t.Fatalf("after Ctrl+Shift+X: %q", got)
	}
	for _, r := range "abc" {
		scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	scr.InjectKey(tcell.KeyLeft, 0, tcell.ModShift)
	scr.InjectKey(tcell.KeyCtrlX, 0, tcell.ModCtrl) // legacy form
	time.Sleep(200 * time.Millisecond)
	if got := text(); got != "ab|c" {
		t.Fatalf("after Ctrl+X: %q", got)
	}
}
