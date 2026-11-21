package ui

import (
	"context"
	"os"
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
	for _, want := range []string{"no connection", "[scratch]", "Messages", "10Quit", "5Run"} {
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
