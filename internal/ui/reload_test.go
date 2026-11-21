package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/exec"
)

func cmdline(scr tcell.SimulationScreen, line string) {
	scr.InjectKey(tcell.KeyF9, 0, tcell.ModNone)
	for _, r := range line {
		scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
}

// TestReloadInstallsUserScripts: a script put into the user command
// directory at run time becomes a command after `reload`; a broken one is
// reported and the other commands keep working.
func TestReloadInstallsUserScripts(t *testing.T) {
	dir := t.TempDir()
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true, UserCmdDir: dir})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	cmdline(scr, "hello")
	waitFor(t, a, scr, "no connection", 2*time.Second) // not a command yet: runs as SQL

	os.WriteFile(filepath.Join(dir, "hello.lua"), []byte(`dbcc.command{
  name = "hello", usage = "hello", help = "greets", args = {},
  run = function(ctx, args) ctx:message("hi from new script") end,
}`), 0o600)
	os.WriteFile(filepath.Join(dir, "broken.lua"), []byte(`dbcc.command{ name = `), 0o600)
	cmdline(scr, "reload")
	waitFor(t, a, scr, "commands reloaded", 3*time.Second)
	scr.InjectKey(tcell.KeyF3, 0, tcell.ModAlt)
	waitFor(t, a, scr, "user command script skipped", 3*time.Second) // broken.lua is reported
	cmdline(scr, "hello")
	waitFor(t, a, scr, "hi from new script", 3*time.Second)
	cmdline(scr, "pwd") // built-ins still work
	waitFor(t, a, scr, "1 pwd", 3*time.Second)
}

func TestInfoMessageHasNoRowCount(t *testing.T) {
	th := NewTheme(256)
	l := NewMessageLog(th)
	l.Add(exec.Message{Info: "commands reloaded"})
	if got := l.GetText(true); strings.Contains(got, "rows") || !strings.Contains(got, "commands reloaded") {
		t.Fatalf("info message rendered as %q", got)
	}
}
