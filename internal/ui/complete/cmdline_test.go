package complete

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dbcc/internal/command"
	"dbcc/internal/dialect"
	"dbcc/internal/dialect/mysql"
	"dbcc/internal/meta"
	"dbcc/internal/script"
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
	return &script.Command{Name: name, Args: args}, true
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
	}
	for _, c := range cases {
		got := commandLine(context.Background(), testCommands, comp, c.line)
		if c.line == "source " || c.line == "source l" {
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
