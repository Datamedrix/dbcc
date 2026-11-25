package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dbcc/internal/branding"
)

// TestMain lets the test binary act as dbcc when DBCC_TEST_MAIN is set.
func TestMain(m *testing.M) {
	if os.Getenv("DBCC_TEST_MAIN") == "1" {
		os.Args = append([]string{"dbcc"}, os.Args[1:]...)
		os.Exit(run())
	}
	os.Exit(m.Run())
}

func TestLicensesFlagPrintsAndExits0(t *testing.T) {
	cmd := exec.Command(os.Args[0], "--licenses")
	cmd.Env = append(os.Environ(), "DBCC_TEST_MAIN=1", "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("dbcc --licenses: %v", err)
	}
	if string(out) != branding.ThirdPartyLicenses {
		t.Fatalf("output differs from the embedded licences (%d bytes)", len(out))
	}
}

// dbccRun runs this test binary as dbcc with a private configuration and
// state directory; it returns stdout, stderr and the exit code.
func dbccRun(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DBCC_TEST_MAIN=1", "HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "cfg"),
		"XDG_STATE_HOME="+filepath.Join(dir, "state"))
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Stdin = strings.NewReader("")
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

// TestHeadlessFilesWithoutProfile runs the real main against files: no
// database and no -p are needed for file commands (A29).
func TestHeadlessFilesWithoutProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the dbcc binary")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "kunden.csv"), []byte("id;name;ort\n1;Anna;Köln\n2;Bob;\n3;\"Cem, Jr.\";Bonn\n4;Dora;Ulm\n"), 0o600)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-c", "head file:kunden.csv 3"}, "id | name     | ort\n---+----------+-----\n 1 | Anna     | Köln\n 2 | Bob      | \n 3 | Cem, Jr. | Bonn\n"},
		{[]string{"-c", "head file:kunden.csv 3", "--format", "csv"}, "id,name,ort\n1,Anna,Köln\n2,Bob,\n3,\"Cem, Jr.\",Bonn\n"},
		{[]string{"-c", "head file:kunden.csv 2", "--format", "tsv"}, "id\tname\tort\n1\tAnna\tKöln\n2\tBob\t\n"},
		{[]string{"-c", "head file:kunden.csv 2", "--format", "json"}, "[\n  {\"id\": \"1\", \"name\": \"Anna\", \"ort\": \"Köln\"},\n  {\"id\": \"2\", \"name\": \"Bob\", \"ort\": \"\"}\n]\n"},
		{[]string{"-c", "wc file:kunden.csv", "--format", "csv"}, "rows,object\n4," + filepath.Join(dir, "kunden.csv") + "\n"},
		{[]string{"-c", "tail file:" + filepath.Join(dir, "kunden.csv") + " 1", "--format", "csv"}, "id,name,ort\n4,Dora,Ulm\n"},
		{[]string{"-c", "open -o delimiter=comma -o header=false kunden.csv", "--format", "csv"}, // the comma splits "Cem, Jr." (a field quoted only after ";" is not quoted)
			"col1,col2\nid;name;ort,\n1;Anna;Köln,\n2;Bob;,\n\"3;\"\"Cem\",\" Jr.\"\";Bonn\"\n4;Dora;Ulm,\n"},
	}
	for _, c := range cases {
		out, errs, code := dbccRun(t, dir, c.args...)
		if code != 0 || out != c.want {
			t.Errorf("%v: exit %d, stderr %q\n got  %q\n want %q", c.args, code, errs, out, c.want)
		}
	}
	out, _, code := dbccRun(t, dir, "-c", "cat file:kunden.csv")
	if code != 0 || !strings.Contains(out, "file:     "+filepath.Join(dir, "kunden.csv")) || !strings.Contains(out, "3 | ort    | no") {
		t.Errorf("cat: %d\n%s", code, out)
	}
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"-c", "wc file:missing.csv"}, 1, "no such file or directory"},
		{[]string{"-c", "open -t xml kunden.csv"}, 1, `open: unknown import type "xml" (types: csv)`},
		{[]string{"-c", "open"}, 1, "open: a file is required without UI"},
		{[]string{"-c", "SELECT 1"}, 2, "not connected"},
		{[]string{"-c", "head"}, 2, "usage: head <table|view|file:path> [n]"},
		{[]string{"-f", "x.sql"}, 2, "headless mode needs a profile"},
	} {
		_, errs, code := dbccRun(t, dir, c.args...)
		if code != c.code || !strings.Contains(errs, c.want) {
			t.Errorf("%v: exit %d (want %d), stderr %q (want %q)", c.args, code, c.code, errs, c.want)
		}
	}
}
