package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func typeText(scr tcell.SimulationScreen, s string) {
	for _, r := range s {
		scr.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

// waitFile polls until the file has content with prefix.
func waitFile(t *testing.T, p, prefix string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var b []byte
	for time.Now().Before(deadline) {
		if b, _ = os.ReadFile(p); len(b) > 0 && strings.HasPrefix(string(b), prefix) {
			return string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: %q", p, b)
	return ""
}

// TestOpenFileFlow: open → file dialog → settings → the grid shows the
// file; paging keys page through it, F5 rereads it, e exports it.
func TestOpenFileFlow(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("n;name\n")
	for i := 1; i <= 25; i++ {
		b.WriteString(strconv.Itoa(i) + ";row" + strconv.Itoa(i) + "\n")
	}
	src := filepath.Join(dir, "kunden.csv")
	os.WriteFile(src, []byte(b.String()), 0o600)
	cfg := emptyConfig()
	cfg.Settings.PageSize, cfg.Settings.FetchSize = 10, 10
	a, scr, stop := startApp(t, cfg, Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	cmdline(scr, "open")
	waitFor(t, a, scr, " Open file ", 3*time.Second)
	waitFor(t, a, scr, " File name ", 3*time.Second)
	// A missing name keeps the dialog open with an error, focus frames intact.
	typeText(scr, filepath.Join(dir, "nope.csv"))
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, a, scr, "nope.csv does not exist", 3*time.Second)
	if c, ok := frameColor(t, a, scr, " File name "); !ok || c != a.theme.BorderFocus {
		t.Errorf("file-name frame %v, want the focus colour", c)
	}
	// Ctrl+U clears the input; then the real file.
	scr.InjectKey(tcell.KeyCtrlU, 0, tcell.ModCtrl)
	typeText(scr, src)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, a, scr, "Open kunden.csv as CSV", 3*time.Second)
	scr.InjectKey(tcell.KeyF2, 0, tcell.ModNone) // defaults: auto delimiter
	lines := waitFor(t, a, scr, "row10", 3*time.Second)
	all := strings.Join(lines, "\n")
	if !strings.Contains(all, "kunden.csv") || strings.Contains(all, "row11") {
		t.Fatalf("first page:\n%s", all)
	}
	// n: next page (read on from the open cursor), p: back (reopens).
	scr.InjectKey(tcell.KeyRune, 'n', tcell.ModNone)
	waitFor(t, a, scr, "row20", 3*time.Second)
	scr.InjectKey(tcell.KeyRune, 'n', tcell.ModNone)
	waitFor(t, a, scr, "row25", 3*time.Second)
	scr.InjectKey(tcell.KeyRune, 'p', tcell.ModNone)
	waitFor(t, a, scr, "row11", 3*time.Second)
	// F5 rereads the file (it changed).
	os.WriteFile(src, []byte("n;name\n1;fresh\n"), 0o600)
	scr.InjectKey(tcell.KeyF5, 0, tcell.ModNone)
	waitFor(t, a, scr, "fresh", 3*time.Second)
	// e exports the file result (here as JSON).
	out := t.TempDir()
	done := make(chan struct{})
	a.tv.QueueUpdate(func() { a.lastSaveDir = out; close(done) })
	<-done
	scr.InjectKey(tcell.KeyRune, 'e', tcell.ModNone)
	waitFor(t, a, scr, "JSON (array of objects)", 3*time.Second)
	scr.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, a, scr, "One object per line", 3*time.Second)
	scr.InjectKey(tcell.KeyF2, 0, tcell.ModNone)
	waitFor(t, a, scr, " File name ", 3*time.Second)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	got := waitFile(t, filepath.Join(out, "kunden.csv.json"), "[")
	if got != "[\n  {\"n\": \"1\", \"name\": \"fresh\"}\n]\n" {
		t.Errorf("export: %q", got)
	}
}

// TestFileCommandsInUI: ll file:… opens a grid without a connection; the
// result is not bound to a session (no form, no WHERE).
func TestFileCommandsInUI(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "t.csv")
	os.WriteFile(src, []byte("a,b\n1,x\n2,y\n"), 0o600)
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	cmdline(scr, "head file:"+src+" 1")
	lines := waitFor(t, a, scr, "1 rows", 3*time.Second) // the status line of the new result tab
	if all := strings.Join(lines, "\n"); !strings.Contains(all, "x") || strings.Contains(all, " y ") {
		t.Errorf("head:\n%s", all)
	}
	type check struct {
		file    bool
		session bool
	}
	ch := make(chan check, 1)
	a.tv.QueueUpdate(func() {
		res := a.results.CurrentResult()
		ch <- check{res != nil && res.File != nil, a.sessionOf(res) != nil}
	})
	if c := <-ch; !c.file || c.session {
		t.Errorf("file result %v, bound to a session %v", c.file, c.session)
	}
	scr.InjectKey(tcell.KeyF4, 0, tcell.ModNone) // no row form for a file
	time.Sleep(100 * time.Millisecond)
	waitFor(t, a, scr, "t.csv", time.Second)
}

// loopImporter yields rows 1..10 quickly and then loops forever in Lua.
const loopImporter = `dbcc.importer{ name = "loop", extensions = { "loop" }, open = function(ctx, src, opts)
  local k = 0
  return { columns = { "n" }, next = function()
    k = k + 1
    if k <= 10 then return { "r" .. k } end
    while true do end
  end }
end }`

// TestFileFetchStuckImporter: the grid's background fetch (worker) runs an
// importer that never returns. F8 stops it; closing the tab stops it
// without waiting; the UI and the worker stay usable (run with -race).
func TestFileFetchStuckImporter(t *testing.T) {
	cmds := t.TempDir()
	os.WriteFile(filepath.Join(cmds, "loop.lua"), []byte(loopImporter), 0o600)
	data := t.TempDir()
	src := filepath.Join(data, "x.loop")
	os.WriteFile(src, []byte("x"), 0o600)
	cfg := emptyConfig()
	cfg.Settings.PageSize, cfg.Settings.FetchSize = 100, 10
	a, scr, stop := startApp(t, cfg, Options{NoSplash: true, UserCmdDir: cmds})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	busy := func() bool { return a.busy.Load() > 0 }
	waitBusy := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for busy() != want {
			if time.Now().After(deadline) {
				t.Fatalf("worker busy = %v, want %v", busy(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// 1. F8 stops the stuck background fetch.
	cmdline(scr, "ll file:"+src)
	waitFor(t, a, scr, "1 x.loop", 3*time.Second)
	waitFor(t, a, scr, "r10", 3*time.Second)
	// The command ends, then the grid fetches the next chunk on the worker
	// and the importer loops there.
	time.Sleep(300 * time.Millisecond)
	waitBusy(true)
	scr.InjectKey(tcell.KeyF8, 0, tcell.ModNone)
	waitBusy(false)
	waitFor(t, a, scr, "interrupted", 3*time.Second)
	// 2. Closing the tab while the importer loops: no wait, no hang.
	cmdline(scr, "ll file:"+src)
	waitFor(t, a, scr, "2 x.loop", 3*time.Second) // the second tab
	time.Sleep(300 * time.Millisecond)
	waitBusy(true)
	scr.InjectKey(tcell.KeyF3, 0, tcell.ModAlt)
	scr.InjectKey(tcell.KeyRune, 'x', tcell.ModAlt) // close the result tab
	waitBusy(false)
	// The worker takes new jobs and the UI answers.
	cmdline(scr, "pwd")
	deadline := time.Now().Add(3 * time.Second)
	for {
		ch := make(chan string, 1)
		a.tv.QueueUpdate(func() { ch <- a.results.Current().Title })
		if title := <-ch; strings.HasSuffix(title, " pwd") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pwd did not run after the tab was closed:\n%s", strings.Join(screenText(scr), "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestFileRerunIntoClosedTab: F5 queued behind a busy worker, then the tab
// is closed: the new result is released, no file stays open.
func TestFileRerunIntoClosedTab(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "r.csv")
	os.WriteFile(src, []byte("a\n1\n2\n"), 0o600)
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	cmdline(scr, "ll file:"+src)
	waitFor(t, a, scr, "1 r.csv", 3*time.Second)
	release := make(chan struct{})
	a.do(func() { <-release })                      // keep the worker busy
	scr.InjectKey(tcell.KeyF5, 0, tcell.ModNone)    // rerun queued
	scr.InjectKey(tcell.KeyRune, 'x', tcell.ModAlt) // close the tab
	time.Sleep(200 * time.Millisecond)
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := make(chan int, 1)
		a.do(func() { n <- a.reg.Runtime().OpenFiles() })
		if <-n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the rerun result of a closed tab keeps its file open")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
