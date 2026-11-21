package history

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dbcc/internal/exec"
)

func TestSensitive(t *testing.T) {
	cases := map[string]bool{
		"CREATE USER x IDENTIFIED BY 'secret'":               true,
		"alter user x identified\n   by 's'":                 true,
		"SET PASSWORD FOR x = 'y'":                           true,
		"select * from users where password_hash = ?":        true,
		"ALTER USER x IDENTIFIED WITH caching_sha2_password": true,
		"SELECT 1":                         false,
		"CREATE TABLE identified (by INT)": false,
	}
	for stmt, want := range cases {
		if got := Sensitive(stmt); got != want {
			t.Errorf("Sensitive(%q) = %v, want %v", stmt, got, want)
		}
	}
}

func TestRecordAndLast(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	h, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	msgs := []exec.Message{
		{Time: t0, Stmt: "SELECT 1", Duration: 1500 * time.Microsecond},
		{Time: t0, Info: "a note without statement"},
		{Time: t0, Stmt: "CREATE USER u IDENTIFIED BY 'pw'"},
		{Time: t0, Stmt: "set password = 'x'"},
		{Time: t0, Stmt: "SELECT nope", Err: errors.New("unknown column")},
		{Time: t0, Stmt: "SELECT 3"},
	}
	for _, m := range msgs {
		if err := h.Record(m, "prof", "/prof/db"); err != nil {
			t.Fatal(err)
		}
	}
	all := h.Last(0)
	if len(all) != 3 {
		t.Fatalf("want 3 entries, got %+v", all)
	}
	for _, e := range all {
		if strings.Contains(strings.ToLower(e.Stmt), "password") || strings.Contains(e.Stmt, "IDENTIFIED") {
			t.Errorf("sensitive statement recorded: %q", e.Stmt)
		}
	}
	if all[0].Stmt != "SELECT 1" || !all[0].OK() || all[0].Duration != 1.5 || all[0].Profile != "prof" || all[0].Path != "/prof/db" || !all[0].Time.Equal(t0) {
		t.Errorf("first entry %+v", all[0])
	}
	if all[1].OK() || all[1].Outcome != "unknown column" {
		t.Errorf("error entry %+v", all[1])
	}
	last := h.Last(2)
	if len(last) != 2 || last[0].Stmt != "SELECT nope" || last[1].Stmt != "SELECT 3" {
		t.Errorf("Last(2) = %+v", last)
	}
	if got := h.Last(10); len(got) != 3 {
		t.Errorf("Last(10) = %d entries", len(got))
	}
	b, _ := os.ReadFile(h.Path())
	if strings.Contains(string(b), "pw") || strings.Contains(string(b), "note without") {
		t.Errorf("file contains filtered content:\n%s", b)
	}
	st, err := os.Stat(h.Path())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v %v", st.Mode(), err)
	}
	// Garbage lines are skipped.
	f, _ := os.OpenFile(h.Path(), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("not json\n")
	f.Close()
	h.Record(exec.Message{Stmt: "SELECT 4"}, "p", "/p")
	if got := h.Last(1); len(got) != 1 || got[0].Stmt != "SELECT 4" || got[0].Time.IsZero() {
		t.Errorf("after garbage: %+v", got)
	}
}

func TestOpenNarrowsMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, nil, 0o644)
	os.Chmod(p, 0o644)
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
}

func TestNilHistory(t *testing.T) {
	var h *History
	if err := h.Record(exec.Message{Stmt: "SELECT 1"}, "", ""); err != nil || h.Last(5) != nil {
		t.Error("nil history must be a no-op")
	}
}
