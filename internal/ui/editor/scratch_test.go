package editor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func typeText(e *Editor, s string) { e.InsertText(s) }

func TestScratchFileLifecycle(t *testing.T) {
	dir := t.TempDir()
	tb := NewTabs()
	tb.SetScratchDir(dir)
	now := time.Date(2026, 11, 21, 22, 30, 12, 0, time.Local)
	tab, err := tb.NewScratch(now, "")
	if err != nil {
		t.Fatal(err)
	}
	if tab.File != "scratch_2026-11-21_22-30-12.sql" || tab.Title != "scratch 2026-11-21 22:30" {
		t.Fatalf("file %q title %q", tab.File, tab.Title)
	}
	if _, err := os.Stat(filepath.Join(dir, NoConnection, tab.File)); err != nil {
		t.Fatal("file not created at once:", err)
	}
	// Same second: unique name.
	tab2, _ := tb.NewScratch(now, "")
	if tab2.File != "scratch_2026-11-21_22-30-12-2.sql" {
		t.Fatalf("second file %q", tab2.File)
	}
	typeText(tab.Editor, "SELECT 1;")
	if !tab.Dirty() {
		t.Fatal("edited scratch not dirty")
	}
	if err := tb.SaveTab(tab); err != nil {
		t.Fatal(err)
	}
	if tab.Dirty() {
		t.Fatal("saved scratch still dirty")
	}
	data, _ := os.ReadFile(filepath.Join(dir, NoConnection, tab.File))
	if string(data) != "SELECT 1;" {
		t.Fatalf("file content %q", data)
	}
	// Close saves first; the file stays and can be re-opened.
	typeText(tab.Editor, "\nSELECT 2;")
	if err := tb.CloseScratch(tb.Index(tab)); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, NoConnection, "scratch_2026-11-21_22-30-12.sql"))
	if !strings.Contains(string(data), "SELECT 2;") {
		t.Fatalf("close did not save: %q", data)
	}
	files, _ := tb.ScratchFiles()
	// Newest change first: the closed (just saved) scratch leads.
	if len(files) != 2 || files[0].Name != "scratch_2026-11-21_22-30-12.sql" || files[0].Preview != "SELECT 1;" ||
		files[0].Open || !files[1].Open || files[0].Conn != NoConnection {
		t.Fatalf("list: %+v", files)
	}
	re, err := tb.OpenScratch("", "scratch_2026-11-21_22-30-12.sql")
	if err != nil || !strings.Contains(re.Editor.Text(), "SELECT 2;") || re.Dirty() {
		t.Fatalf("reopen: %v %q dirty=%v", err, re.Editor.Text(), re.Dirty())
	}
	if again, _ := tb.OpenScratch("", re.File); again != re {
		t.Fatal("opening an open scratch must focus its tab")
	}
	if _, err := tb.OpenScratch("", "../../etc/passwd"); err == nil {
		t.Fatal("path outside the scratch directory accepted")
	}
}

func TestDirtyScratchCannotCloseWhenSaveFails(t *testing.T) {
	dir := t.TempDir()
	tb := NewTabs()
	tb.SetScratchDir(dir)
	tab, _ := tb.NewScratch(time.Now(), "")
	typeText(tab.Editor, "important")
	os.Chmod(filepath.Join(dir, NoConnection), 0o500) // saving fails
	defer os.Chmod(filepath.Join(dir, NoConnection), 0o700)
	if err := tb.CloseScratch(tb.Index(tab)); err == nil {
		t.Fatal("dirty scratch closed although it could not be saved")
	}
	if tb.Index(tab) < 0 || tab.Editor.Text() != "important" {
		t.Fatal("tab lost")
	}
}

func TestScratchSessionAndLegacyMigration(t *testing.T) {
	state := t.TempDir()
	legacy := filepath.Join(state, "scratch")
	os.MkdirAll(legacy, 0o700)
	os.WriteFile(filepath.Join(legacy, "scratch-1.sql"), []byte("SELECT legacy;"), 0o600)
	os.WriteFile(filepath.Join(legacy, "scratch-2.sql"), []byte("  "), 0o600)
	os.WriteFile(filepath.Join(legacy, "scratch.json"), []byte(`{}`), 0o600)
	dir := filepath.Join(state, "scratches")

	tb := NewTabs()
	tb.SetScratchDir(dir)
	if err := tb.LoadSession(legacy); err != nil {
		t.Fatal(err)
	}
	if tb.Len() != 1 || tb.Current().Editor.Text() != "SELECT legacy;" {
		t.Fatalf("migration: %d tabs", tb.Len())
	}
	if !regexp.MustCompile(`^scratch_\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}\.sql$`).MatchString(tb.Current().File) {
		t.Fatalf("migrated name %q", tb.Current().File)
	}
	if _, err := os.Stat(filepath.Join(legacy, "scratch-1.sql")); err == nil {
		t.Fatal("legacy file left behind")
	}
	second, _ := tb.NewScratch(time.Now().Add(time.Second), "")
	typeText(second.Editor, "SELECT 2;\nSELECT 3;")
	second.Editor.SetCursor(2, 3)
	if err := tb.SaveSession(); err != nil {
		t.Fatal(err)
	}

	tb2 := NewTabs()
	tb2.SetScratchDir(dir)
	if err := tb2.LoadSession(legacy); err != nil {
		t.Fatal(err)
	}
	if tb2.Len() != 2 || tb2.Current().File != second.File {
		t.Fatalf("restore: %d tabs, current %q", tb2.Len(), tb2.Current().File)
	}
	if l, c := tb2.Current().Editor.Cursor(); l != 2 || c != 3 {
		t.Fatalf("cursor %d:%d", l, c)
	}
	if err := tb2.DeleteScratch("", second.File); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, NoConnection, second.File)); err == nil || tb2.Len() != 1 {
		t.Fatal("delete failed")
	}
}

func TestRenameScratchUniquePerConnection(t *testing.T) {
	dir := t.TempDir()
	tb := NewTabs()
	tb.SetScratchDir(dir)
	now := time.Date(2026, 11, 21, 23, 0, 0, 0, time.Local)
	a, _ := tb.NewScratch(now, "prod")
	b, _ := tb.NewScratch(now.Add(time.Second), "prod")
	c, _ := tb.NewScratch(now, "dev")
	typeText(a.Editor, "SELECT 'unsaved';")
	if err := tb.RenameScratch("prod", a.File, "Monthly report"); err != nil {
		t.Fatal(err)
	}
	if a.File != "Monthly report.sql" || a.Title != "Monthly report" {
		t.Fatalf("tab did not follow the rename: %q %q", a.File, a.Title)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "prod", "Monthly report.sql")); string(data) != "SELECT 'unsaved';" {
		t.Fatalf("pending edits lost by the rename: %q", data)
	}
	if err := tb.RenameScratch("prod", b.File, "monthly REPORT"); err == nil {
		t.Fatal("duplicate name (case-insensitive) in the same connection accepted")
	}
	if err := tb.RenameScratch("dev", c.File, "Monthly report"); err != nil {
		t.Fatalf("same name in another connection must be allowed: %v", err)
	}
	for _, bad := range []string{"", "../x", "a/b", ".hidden", strings.Repeat("x", 101)} {
		if err := tb.RenameScratch("prod", b.File, bad); err == nil {
			t.Errorf("invalid name %q accepted", bad)
		}
	}
	files, _ := tb.ScratchFiles()
	got := map[string]bool{}
	for _, f := range files {
		got[f.Conn+"/"+f.Name] = true
	}
	for _, want := range []string{"prod/Monthly report.sql", "dev/Monthly report.sql", "prod/" + b.File} {
		if !got[want] {
			t.Errorf("listing lacks %s: %v", want, got)
		}
	}
	// Session restore keeps the connection of each scratch.
	if err := tb.SaveSession(); err != nil {
		t.Fatal(err)
	}
	tb2 := NewTabs()
	tb2.SetScratchDir(dir)
	if err := tb2.LoadSession(""); err != nil {
		t.Fatal(err)
	}
	if tb2.Len() != 3 {
		t.Fatalf("restored %d tabs", tb2.Len())
	}
}
