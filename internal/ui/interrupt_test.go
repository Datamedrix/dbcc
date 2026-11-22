package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// TestF8InterruptsEndlessLuaLoop: a user script stuck in an endless loop
// is stopped with F8; further commands work; a script error shows the Lua
// stack trace in the message log.
func TestF8InterruptsEndlessLuaLoop(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "spin.lua"), []byte(`dbcc.command{
  name = "spin", usage = "spin", help = "loops forever", args = {},
  run = function(ctx, args) while true do end end,
}`), 0o600)
	os.WriteFile(filepath.Join(dir, "boom.lua"), []byte(`local function inner() error("kaputt") end
dbcc.command{
  name = "boom", usage = "boom", help = "fails", args = {},
  run = function(ctx, args) inner() end,
}`), 0o600)
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true, UserCmdDir: dir})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	// Wait until start-up (loading the commands) has finished: the
	// "running…" of the start-up job must not be mistaken for spin's.
	cmdline(scr, "pwd")
	waitFor(t, a, scr, "1 pwd", 3*time.Second)
	cmdline(scr, "spin")
	waitFor(t, a, scr, "running…", 3*time.Second)
	time.Sleep(200 * time.Millisecond) // spin is inside its loop
	scr.InjectKey(tcell.KeyF8, 0, tcell.ModNone)
	waitFor(t, a, scr, "spin: interrupted", 3*time.Second)
	cmdline(scr, "pwd")
	waitFor(t, a, scr, "2 pwd", 3*time.Second) // further commands still work
	cmdline(scr, "boom")
	scr.InjectKey(tcell.KeyF3, 0, tcell.ModAlt)
	scr.InjectKey(tcell.KeyRune, ']', tcell.ModNone) // Messages tab
	waitFor(t, a, scr, "Lua stack:", 3*time.Second)
}
