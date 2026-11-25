package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/config"
)

func TestListDirOrderAndHidden(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"beta", "Alpha", ".git"} {
		os.Mkdir(filepath.Join(dir, d), 0o755)
	}
	for _, f := range []string{"b.csv", "A.CSV", "notes.txt", ".hidden.csv"} {
		os.WriteFile(filepath.Join(dir, f), []byte("12345"), 0o600)
	}
	os.Symlink(filepath.Join(dir, "beta"), filepath.Join(dir, "link"))
	names := func(es []fileEntry) string {
		var out []string
		for _, e := range es {
			n := e.Name
			if e.Dir {
				n += "/"
			}
			if !e.Match {
				n += "(dim)"
			}
			out = append(out, n)
		}
		return strings.Join(out, " ")
	}
	es, err := listDir(dir, false, []string{"csv"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(es); got != "Alpha/ beta/ link/ A.CSV b.csv notes.txt(dim)" {
		t.Errorf("listing: %s", got)
	}
	if es[3].Size != 5 || es[3].Mod.IsZero() {
		t.Errorf("size/date: %+v", es[3])
	}
	es, _ = listDir(dir, true, nil)
	if got := names(es); got != ".git/ Alpha/ beta/ link/ .hidden.csv A.CSV b.csv notes.txt" {
		t.Errorf("with hidden: %s", got)
	}
	sub, _ := subdirs(dir, false)
	if strings.Join(sub, " ") != "Alpha beta link" {
		t.Errorf("subdirs: %v", sub)
	}
	if _, err := listDir(filepath.Join(dir, "nope"), false, nil); err == nil {
		t.Error("missing directory listed")
	}
}

func TestFileDialogPaths(t *testing.T) {
	cases := []struct {
		in   string
		exts []string
		want string
	}{
		{"customers", []string{"csv"}, "customers.csv"},
		{"customers.txt", []string{"csv"}, "customers.txt"},
		{"dir/customers", []string{"csv", "txt"}, "dir/customers.csv"},
		{"a.b/customers", []string{"csv"}, "a.b/customers.csv"},
		{".hidden", []string{"csv"}, ".hidden.csv"},
		{"plain", nil, "plain"},
	}
	for _, c := range cases {
		if got := withExtension(c.in, c.exts); got != c.want {
			t.Errorf("withExtension(%q, %v) = %q, want %q", c.in, c.exts, got, c.want)
		}
	}
	if !hasExtension("X.Json", []string{"json"}) || hasExtension("x.jsonl", []string{"json"}) || !hasExtension("x", nil) {
		t.Error("hasExtension")
	}

	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	home := config.HomeDir()
	for in, want := range map[string]struct {
		path  string
		isDir bool
	}{
		"out.csv":                       {filepath.Join(dir, "out.csv"), false},
		"sub":                           {filepath.Join(dir, "sub"), true},
		"sub/x.csv":                     {filepath.Join(dir, "sub", "x.csv"), false},
		"../" + filepath.Base(dir):      {dir, true},
		filepath.Join(dir, "sub") + "/": {filepath.Join(dir, "sub"), true},
		"/tmp/../etc/x":                 {"/etc/x", false},
		"~/x.csv":                       {filepath.Join(home, "x.csv"), false},
		"  sub  ":                       {filepath.Join(dir, "sub"), true},
	} {
		p, isDir := resolveInput(in, dir)
		if p != want.path || isDir != want.isDir {
			t.Errorf("resolveInput(%q) = %q, %v; want %q, %v", in, p, isDir, want.path, want.isDir)
		}
	}
	if err := checkSaveTarget(filepath.Join(dir, "sub", "x.csv")); err != nil {
		t.Errorf("existing parent: %v", err)
	}
	if err := checkSaveTarget(filepath.Join(dir, "nope", "x.csv")); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing parent: %v", err)
	}
	if got := startDir(filepath.Join(dir, "nope"), dir); got != dir {
		t.Errorf("startDir fallback: %q", got)
	}
	if got := startDir(filepath.Join(dir, "sub"), dir); got != filepath.Join(dir, "sub") {
		t.Errorf("startDir: %q", got)
	}
	if wd, _ := os.Getwd(); startDir("", "") != wd {
		t.Errorf("startDir default: %q", startDir("", ""))
	}
}

func TestExportKeyBinding(t *testing.T) {
	k := DefaultKeymap()
	if a := k.Action(PaneGrid, tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone)); a != "export" {
		t.Errorf("grid e = %q", a)
	}
	if a := k.Action(PaneEditor, tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone)); a == "export" {
		t.Error("e exports in the editor")
	}
	if err := k.Apply(map[string]map[string]string{"grid": {"export": "alt+e"}}, ""); err != nil {
		t.Fatal(err)
	}
	if a := k.Action(PaneGrid, tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModAlt)); a != "export" {
		t.Errorf("rebound alt+e = %q", a)
	}
	if a := k.Action(PaneGrid, tcell.NewEventKey(tcell.KeyRune, 'e', tcell.ModNone)); a == "export" {
		t.Error("old key still bound")
	}
	// The F-key footer of the grid is unchanged.
	if f := DefaultKeymap().Footer(PaneGrid); f[2] != "Pivot" || f[5] != "NextPg" {
		t.Errorf("footer %v", f)
	}
}

// resultScript is a user command that opens a result tab without a
// database connection.
const resultScript = `dbcc.command{ name = "mk", usage = "mk", help = "test result", args = {},
  run = function(ctx) ctx:rows({ "id", "name" }, { { 1, "a,b" }, { 2, nil } }, { title = "customers" }) end }`

func startWithResult(t *testing.T) (*App, tcell.SimulationScreen, func()) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mk.lua"), []byte(resultScript), 0o600)
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true, UserCmdDir: dir})
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	cmdline(scr, "mk")
	waitFor(t, a, scr, "3Pivot", 3*time.Second) // the new result tab has the focus
	waitFor(t, a, scr, "a,b", 3*time.Second)
	return a, scr, stop
}

// TestExportKeyOpensTypeList: e in a result tab runs export, which lists
// the export types first.
func TestExportKeyOpensTypeList(t *testing.T) {
	a, scr, stop := startWithResult(t)
	defer stop()
	scr.InjectKey(tcell.KeyRune, 'e', tcell.ModNone)
	waitFor(t, a, scr, "Export as", 3*time.Second)
	waitFor(t, a, scr, "CSV (comma-separated values)", 3*time.Second)
	waitFor(t, a, scr, "JSON (array of objects)", 3*time.Second)
	scr.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	waitFor(t, a, scr, "3Pivot", 3*time.Second)
	scr.InjectKey(tcell.KeyF3, 0, tcell.ModAlt)
}

// TestExportEndToEnd drives type list -> settings -> file dialog and
// checks the written file.
func TestExportEndToEnd(t *testing.T) {
	a, scr, stop := startWithResult(t)
	defer stop()
	out := t.TempDir()
	done := make(chan struct{})
	a.tv.QueueUpdate(func() { a.lastSaveDir = out; close(done) })
	<-done
	scr.InjectKey(tcell.KeyRune, 'e', tcell.ModNone)
	waitFor(t, a, scr, "CSV (comma-separated values)", 3*time.Second)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // CSV is the first type
	waitFor(t, a, scr, "Export as CSV (comma-separated values)", 3*time.Second)
	waitFor(t, a, scr, "Delimiter", 3*time.Second)
	waitFor(t, a, scr, "Text for NULL", 3*time.Second)
	// Header row is the second item: Tab to it and switch it off.
	scr.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	scr.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	scr.InjectKey(tcell.KeyF2, 0, tcell.ModNone)
	waitFor(t, a, scr, " File name ", 3*time.Second)
	lines := waitFor(t, a, scr, "customers.csv", 3*time.Second)
	if !strings.Contains(strings.Join(lines, "\n"), "/"+filepath.Base(filepath.Dir(out))+"/"+filepath.Base(out)) {
		t.Errorf("file dialog does not show %s:\n%s", out, strings.Join(lines, "\n"))
	}
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	p := filepath.Join(out, "customers.csv")
	deadline := time.Now().Add(3 * time.Second)
	var b []byte
	for time.Now().Before(deadline) {
		if b, _ = os.ReadFile(p); len(b) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(b) != "1,\"a,b\"\n2,\n" {
		t.Fatalf("exported %q", b)
	}
	// A second export to the same file asks before overwriting.
	scr.InjectKey(tcell.KeyRune, 'e', tcell.ModNone)
	waitFor(t, a, scr, "JSON (array of objects)", 3*time.Second)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, a, scr, "Delimiter", 3*time.Second)
	scr.InjectKey(tcell.KeyF2, 0, tcell.ModNone)
	waitFor(t, a, scr, " File name ", 3*time.Second)
	waitFor(t, a, scr, "customers.csv", 3*time.Second)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, a, scr, "Overwrite?", 3*time.Second) // the modal may wrap a long path before it
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // Yes
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ = os.ReadFile(p); strings.HasPrefix(string(b), "id,") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(b) != "id,name\n1,\"a,b\"\n2,\n" {
		t.Fatalf("overwritten %q", b)
	}
}

// frameColor returns the foreground colour of the frame cell just left of
// the title text on the screen ("" when the title is not shown).
func frameColor(t *testing.T, a *App, scr tcell.SimulationScreen, title string) (tcell.Color, bool) {
	t.Helper()
	type res struct {
		c  tcell.Color
		ok bool
	}
	ch := make(chan res, 1)
	a.tv.QueueUpdate(func() {
		lines := screenText(scr)
		for y, l := range lines {
			if i := strings.Index(l, title); i > 0 {
				x := len([]rune(l[:i])) - 1
				_, _, st, _ := scr.GetContent(x, y)
				fg, _, _ := st.Decompose()
				ch <- res{fg, true}
				return
			}
		}
		ch <- res{}
	})
	r := <-ch
	return r.c, r.ok
}

// TestFileDialogFocusFrames: each area of the file dialog has a frame;
// the focused one is drawn in the focus colour, following Tab/Shift+Tab.
func TestFileDialogFocusFrames(t *testing.T) {
	a, scr, stop := startWithResult(t)
	defer stop()
	out := t.TempDir()
	done := make(chan struct{})
	a.tv.QueueUpdate(func() { a.lastSaveDir = out; close(done) })
	<-done
	scr.InjectKey(tcell.KeyRune, 'e', tcell.ModNone)
	waitFor(t, a, scr, "CSV (comma-separated values)", 3*time.Second)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitFor(t, a, scr, "Delimiter", 3*time.Second)
	scr.InjectKey(tcell.KeyF2, 0, tcell.ModNone)
	waitFor(t, a, scr, " File name ", 3*time.Second)
	if a.theme.BorderFocus == a.theme.Border {
		t.Fatal("test theme has no distinct focus colour")
	}
	titles := []string{" File name ", " Files ", " Folders "}
	check := func(step string, focused string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			bad := ""
			for _, ti := range titles {
				c, ok := frameColor(t, a, scr, ti)
				want := a.theme.Border
				if ti == focused {
					want = a.theme.BorderFocus
				}
				if !ok || c != want {
					bad += fmt.Sprintf(" %q=%v(want %v)", ti, c, want)
				}
			}
			if bad == "" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s:%s\n%s", step, bad, strings.Join(screenText(scr), "\n"))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	check("open", " File name ")
	scr.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	check("Tab", " Files ")
	scr.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	check("Tab Tab", " Folders ")
	scr.InjectKey(tcell.KeyBacktab, 0, tcell.ModNone)
	check("Shift+Tab", " Files ")
	scr.InjectKey(tcell.KeyBacktab, 0, tcell.ModNone)
	check("Shift+Tab Shift+Tab", " File name ")
	scr.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
}
