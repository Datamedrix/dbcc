package complete

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dbcc/internal/command"
	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/dialect/mysql"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/script"
	"dbcc/internal/session"
)

var _ commandSource = (*command.Registry)(nil)

type fakeCommands map[string][]string // name -> arg decl

func (f fakeCommands) Complete(prefix string) []string {
	var out []string
	for n := range f {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (f fakeCommands) Lookup(name string) (*script.Command, bool) {
	decl, ok := f[name]
	if !ok {
		return nil, false
	}
	args, err := script.ParseArgSpecs(decl)
	if err != nil {
		panic(err)
	}
	return &script.Command{Name: name, Args: args, Flags: testFlags[name]}, true
}

var catKinds = []string{"table", "view", "procedure", "function", "trigger", "event", "materialized view", "mview"}

// testFlags mirrors the flag declarations of the default commands.
var testFlags = map[string][]script.FlagSpec{
	"cat":    {{Name: "t", Type: "string", Values: catKinds}},
	"cp":     {{Name: "a", Type: script.FlagBool}},
	"tail":   {{Name: "f", Type: script.FlagBool}, {Name: "i", Type: "string"}, {Name: "k", Type: "string"}},
	"kill":   {{Name: "q", Type: script.FlagBool}},
	"export": {{Name: "f", Type: script.FlagBool}},
	"run":    {{Name: "c", Type: "profile"}},
}

var testCommands = fakeCommands{
	"cd":     {"path?"},
	"cat":    {"object"},
	"cp":     {"object", "object"},
	"ll":     {"path?"},
	"head":   {"object", "int?"},
	"source": {"file"},
	"open":   {"profile"},
	"grep":   {"string", "object..."},
	"tail":   {"object", "int?"},
	"kill":   {"int"},
	"export": {"file"},
	"run":    {"file"},
}

func cmdCompleter(t *testing.T) *Completer {
	t.Helper()
	comp := mysqlCompleter(shopLoader())
	comp.Profiles = func() []string { return []string{"dev", "prod", "my server"} }
	comp.DialectOf = func(conn string) dialect.Dialect {
		if conn == "dev" {
			return &mysql.MySQL{}
		}
		return nil
	}
	return comp
}

func TestCommandLine(t *testing.T) {
	comp := cmdCompleter(t)
	dir := t.TempDir()
	for _, f := range []string{"load.sql", "lib.lua", ".hidden", "my file.sql"} {
		os.WriteFile(filepath.Join(dir, f), nil, 0o644)
	}
	os.Mkdir(filepath.Join(dir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "scripts", "a.sql"), nil, 0o644)
	comp.Dir = dir

	cases := []struct {
		line string
		want []string
	}{
		{"c", []string{"cat", "cd", "cp"}},
		{"  he", []string{"  head"}},
		{"zz", nil},
		{"cat ", []string{"cat Customers", "cat orders", `cat "order items"`, "cat select", "cat v_open", "cat do_thing", "cat f_total"}},
		{"cat or", []string{"cat orders", `cat "order items"`}},
		{`cat "order i`, []string{`cat "order items"`}},
		{"cat hr.", []string{"cat hr.staff", "cat hr.hire"}},
		{"cat /dev/hr/s", []string{"cat /dev/hr/staff"}},
		{"cat /", []string{"cat /dev/", "cat /prod/", `cat "/my server/"`}},
		{"cat /dev/", []string{"cat /dev/hr/", "cat /dev/shop/"}},
		{"cat ../hr/h", []string{"cat ../hr/hire"}},
		{"cp orders cu", []string{"cp orders Customers"}},
		{"cp a b c", nil},      // too many arguments
		{"head orders 1", nil}, // int has no candidates
		{"grep foo or", []string{"grep foo orders", `grep foo "order items"`}},
		{"grep foo x orders s", []string{"grep foo x orders select"}}, // variadic
		{"grep f", nil}, // string
		{"cd ", []string{"cd .."}},
		{"cd ..", []string{"cd .."}},
		{"cd ../", []string{"cd ../hr", "cd ../shop"}},
		{"cd /", []string{"cd /dev", "cd /prod", `cd "/my server"`}},
		{"cd /d", []string{"cd /dev"}},
		{"cd /dev/s", []string{"cd /dev/shop"}},
		{"cd /prod/", nil}, // not connected: no databases
		{"open p", []string{"open prod"}},
		{"open ", []string{"open dev", "open prod", `open "my server"`}},
		{"source ", []string{"source lib.lua", "source load.sql", `source "my file.sql"`, "source scripts/"}},
		{"source l", []string{"source lib.lua", "source load.sql"}},
		{"source .h", []string{"source .hidden"}},
		{"source scripts/", []string{"source scripts/a.sql"}},
		{"SELECT * FROM or", []string{"SELECT * FROM orders", "SELECT * FROM `order items`"}},
		// Flags are skipped when counting positionals.
		{"cat -t view ", []string{"cat -t view Customers", "cat -t view orders", `cat -t view "order items"`, "cat -t view select", "cat -t view v_open", "cat -t view do_thing", "cat -t view f_total"}},
		{"cat -t view v_", []string{"cat -t view v_open"}},
		{"cat -t=view or", []string{"cat -t=view orders", `cat -t=view "order items"`}},
		{"cat -t view orders ", nil}, // no further argument
		// Flag values from the declaration.
		{"cat -t ", []string{"cat -t table", "cat -t view", "cat -t procedure", "cat -t function", "cat -t trigger", "cat -t event", `cat -t "materialized view"`, "cat -t mview"}},
		{"cat -t m", []string{`cat -t "materialized view"`, "cat -t mview"}},
		{"cat -t=v", []string{"cat -t=view"}},
		{"tail -f -i ", nil},                  // string value without declared values
		{"run -c p", []string{"run -c prod"}}, // value completed by its type
		{"cp -a ", []string{"cp -a Customers", "cp -a orders", `cp -a "order items"`, "cp -a select", "cp -a v_open", "cp -a do_thing", "cp -a f_total"}},
		{"cp -a orders cu", []string{"cp -a orders Customers"}},
		{"tail -f -i 2 or", []string{"tail -f -i 2 orders", `tail -f -i 2 "order items"`}},
		{"tail -f -i 2 orders 5", nil},
		{"kill -q ", nil},
		{"kill -q 1", nil},
		{"export -f l", []string{"export -f lib.lua", "export -f load.sql"}},
		{"export -f scripts/", []string{"export -f scripts/a.sql"}},
		// Flag names.
		{"tail -", []string{"tail -f", "tail -i", "tail -k"}},
		{"tail -f -", []string{"tail -f -f", "tail -f -i", "tail -f -k"}},
		{"tail --", []string{"tail --f", "tail --i", "tail --k"}},
		{"tail -k", []string{"tail -k"}},
		{"kill -", []string{"kill -q"}},
		{"cat -x", nil},
		{"tail orders -", nil}, // flags only before positionals
		{"cd -", nil},          // no declared flags: positional
		// "--" ends the flags.
		{"cp -- or", []string{"cp -- orders", `cp -- "order items"`}},
		{"cp -- -", nil},
		{"cp -a -- orders cu", []string{"cp -a -- orders Customers"}},
		{"cat -- -t ", nil},
	}
	for _, c := range cases {
		got := commandLine(context.Background(), testCommands, comp, c.line)
		if c.line == "source " || c.line == "source l" || strings.HasPrefix(c.line, "export ") {
			sort.Strings(got)
			sort.Strings(c.want)
		}
		if !eq(got, c.want) {
			t.Errorf("%q\n got  %q\n want %q", c.line, got, c.want)
		}
	}
}

func TestCommandLineAtRoot(t *testing.T) {
	comp := &Completer{Cache: meta.NewCache(nil), Profiles: func() []string { return []string{"dev", "prod"} }}
	if got := commandLine(context.Background(), testCommands, comp, "cd "); !eq(got, []string{"cd dev", "cd prod"}) {
		t.Fatalf("cd at root: %q", got)
	}
	if got := commandLine(context.Background(), testCommands, comp, "cat d"); !eq(got, []string{"cat dev/"}) {
		t.Fatalf("cat at root: %q", got)
	}
	if got := commandLine(context.Background(), testCommands, comp, "SELECT 1"); got != nil {
		t.Fatalf("SQL at root: %q", got)
	}
	if got := commandLine(context.Background(), testCommands, nil, "cd x"); got != nil {
		t.Fatalf("nil completer: %q", got)
	}
	if got := CommandLine(nil, comp, "c"); got != nil {
		t.Fatal("nil registry")
	}
}

func TestLastWordStart(t *testing.T) {
	cases := map[string]int{
		"":                  0,
		"cd":                0,
		"cd ":               3,
		"cat a b":           6,
		`cat "a b`:          4,
		`cat "a b" c`:       10,
		`cat a\ b`:          4,
		`cat 'x y' "z \" w`: 10,
	}
	for line, want := range cases {
		if got := lastWordStart(line); got != want {
			t.Errorf("lastWordStart(%q) = %d, want %d", line, got, want)
		}
	}
	if unquoteWord(`"order i`) != "order i" || unquoteWord(`a\ b`) != "a b" || unquoteWord("") != "" {
		t.Fatal("unquoteWord")
	}
}

type nopHost struct{}

func (nopHost) Result(*exec.Result)                {}
func (nopHost) Text(_, _, _ string)                {}
func (nopHost) Message(exec.Message)               {}
func (nopHost) Confirm(string) (bool, error)       { return false, nil }
func (nopHost) Pick(string, []string) (int, error) { return -1, script.ErrNoUI }
func (nopHost) Form(string, []script.OptionSpec, map[string]any) (map[string]any, bool, error) {
	return nil, false, script.ErrNoUI
}
func (nopHost) OpenFile(script.SaveFileOptions) (string, bool, error) {
	return "", false, script.ErrNoUI
}
func (nopHost) SaveFile(script.SaveFileOptions) (string, bool, error) {
	return "", false, script.ErrNoUI
}
func (nopHost) EditorSet(string) error                             { return script.ErrNoUI }
func (nopHost) EditorOpen(string, string, *script.ObjectRef) error { return nil }
func (nopHost) About() error                                       { return nil }
func (nopHost) ActiveResult() (*exec.Result, exec.View)            { return nil, nil }

// TestCommandLineDefaults runs against the embedded default commands, so
// the flag declarations of the Lua scripts are covered too.
func TestCommandLineDefaults(t *testing.T) {
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	rt := script.New(script.Env{Workspace: session.NewWorkspace(cfg, nil), Host: nopHost{}})
	defer rt.Close()
	reg := command.New(rt)
	if err := reg.LoadDefaults(); err != nil {
		t.Fatal(err)
	}
	comp := cmdCompleter(t)
	dir := t.TempDir()
	for _, f := range []string{"load.sql", "lib.lua"} {
		os.WriteFile(filepath.Join(dir, f), nil, 0o644)
	}
	os.Mkdir(filepath.Join(dir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "scripts", "a.sql"), nil, 0o644)
	comp.Dir = dir
	cases := []struct {
		line string
		want []string
	}{
		{"cat -t ", []string{"cat -t table", "cat -t view", "cat -t procedure", "cat -t function", "cat -t trigger", "cat -t event", `cat -t "materialized view"`, "cat -t mview"}},
		{"cat -t view v", []string{"cat -t view v_open"}},
		{"cp -a or", []string{"cp -a orders", `cp -a "order items"`}},
		{"cp -a orders x", nil}, // destination is a string
		{"tail -", []string{"tail -f", "tail -i", "tail -k"}},
		{"tail -f -i 2 or", []string{"tail -f -i 2 orders", `tail -f -i 2 "order items"`}},
		{"kill -q ", nil},
		{"kill -", []string{"kill -q"}},
		{"export -", []string{"export -f", "export -o", "export -t"}},
		// flag_values = { t = "exporters" }: the registered export types (A28).
		{"export -t ", []string{"export -t csv", "export -t json"}},
		{"export -t j", []string{"export -t json"}},
		{"export -t=c", []string{"export -t=csv"}},
		// flag_values = { t = "importers" } and file:<path> objects (A29).
		{"open -t ", []string{"open -t csv"}},
		{"ll file:l", []string{"ll file:lib.lua", "ll file:load.sql"}},
		{"head file:s", []string{"head file:scripts/"}},
		{"wc file:scripts/", []string{"wc file:scripts/a.sql"}},
		{"open file:l", []string{"open file:lib.lua", "open file:load.sql"}}, // a "file" argument
		{"export file:s", []string{"export file:scripts/"}},
	}
	for _, c := range cases {
		if got := commandLine(context.Background(), reg, comp, c.line); !eq(got, c.want) {
			t.Errorf("%q\n got  %q\n want %q", c.line, got, c.want)
		}
	}
}
