package script

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/history"
	"dbcc/internal/session"
)

func TestFlagsParsing(t *testing.T) {
	rt, h := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `
dbcc.command{ name = "cp", args = { "object", "string" }, flags = { a = "bool", n = "int", t = "string", ["dry-run"] = "bool" },
  run = function(ctx, args, flags)
    local keys = {}
    for k, v in pairs(flags) do keys[#keys + 1] = k .. "=" .. tostring(v) end
    table.sort(keys)
    ctx:text(table.concat(args, ",") .. "|" .. table.concat(keys, ","))
  end }
dbcc.command{ name = "cd", args = { "path?" }, run = function(ctx, args, flags)
  ctx:text(tostring(args[1]) .. "|" .. tostring(next(flags))) end }`)
	cases := []struct {
		cmd  string
		args []string
		want string // "" = usage error
	}{
		{"cp", []string{"a", "b"}, "a,b|"},
		{"cp", []string{"-a", "a", "b"}, "a,b|a=true"},
		{"cp", []string{"--a", "a", "b"}, "a,b|a=true"},
		{"cp", []string{"-t", "view", "-a", "x", "y"}, "x,y|a=true,t=view"},
		{"cp", []string{"-t=view", "x", "y"}, "x,y|t=view"},
		{"cp", []string{"-n", "5", "x", "y"}, "x,y|n=5"},
		{"cp", []string{"-dry-run", "x", "y"}, "x,y|dry-run=true"},
		{"cp", []string{"--", "-a", "y"}, "-a,y|"},
		{"cp", []string{"x", "-a"}, "x,-a|"}, // flags only before positionals
		{"cp", []string{"-", "y"}, "-,y|"},
		{"cp", []string{"-5", "y"}, "-5,y|"},
		{"cp", []string{"-x", "a", "b"}, ""},
		{"cp", []string{"-a=1", "a", "b"}, ""},
		{"cp", []string{"-t"}, ""},
		{"cp", []string{"-n", "x", "a", "b"}, ""},
		{"cp", []string{"-a", "b"}, ""}, // arity counts positionals only
		{"cd", []string{"-"}, "-|nil"},
		{"cd", []string{"-x"}, "-x|nil"}, // no declared flags: all positional
	}
	for _, c := range cases {
		h.texts = nil
		err := run(t, rt, c.cmd, c.args...)
		if c.want == "" {
			var ue *UsageError
			if !errors.As(err, &ue) {
				t.Errorf("%s %v: want usage error, got %v", c.cmd, c.args, err)
			}
			continue
		}
		if err != nil || len(h.texts) != 1 || h.texts[0] != c.want {
			t.Errorf("%s %v: got %q, %v; want %q", c.cmd, c.args, h.texts, err, c.want)
		}
	}
	cp, _ := rt.Command("cp")
	if cp.Usage != "cp [-a] [-dry-run] [-n int] [-t string] <object> <string>" {
		t.Errorf("default usage %q", cp.Usage)
	}
}

func TestFlagsDeclaration(t *testing.T) {
	cases := map[string]string{
		`dbcc.command{ name = "x", flags = { a = "boolean" }, run = function() end }`:   `unknown type "boolean"`,
		`dbcc.command{ name = "x", flags = { ["1a"] = "bool" }, run = function() end }`: `invalid flag name`,
		`dbcc.command{ name = "x", flags = { "a" }, run = function() end }`:             `flags must map`,
		`dbcc.command{ name = "x", flags = "a", run = function() end }`:                 `"flags" must be a table`,
	}
	for src, want := range cases {
		rt, _ := newTestRuntime()
		if _, err := rt.Load("test.lua", src, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", src, want, err)
		}
		rt.Close()
	}
}

func TestExpect(t *testing.T) {
	rt, _ := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "cat", args = { "object", "int?" }, flags = { t = "string", a = "bool" }, run = function() end }
dbcc.command{ name = "v", args = { "path", "string..." }, run = function() end }`)
	cat, _ := rt.Command("cat")
	v, _ := rt.Command("v")
	cases := []struct {
		c     *Command
		prior []string
		want  Expectation
	}{
		{cat, nil, Expectation{FlagAllowed: true, Positional: 0, Type: ArgObject}},
		{cat, []string{"-t"}, Expectation{FlagValue: "t", Positional: -1, Type: ArgString}},
		{cat, []string{"-t", "view"}, Expectation{FlagAllowed: true, Positional: 0, Type: ArgObject}},
		{cat, []string{"-a", "-t=view"}, Expectation{FlagAllowed: true, Positional: 0, Type: ArgObject}},
		{cat, []string{"-a", "items"}, Expectation{Positional: 1, Type: ArgInt, Optional: true}},
		{cat, []string{"--"}, Expectation{Positional: 0, Type: ArgObject}},
		{cat, []string{"items", "3"}, Expectation{Positional: -1}},
		{v, []string{"-x"}, Expectation{Positional: 1, Type: ArgString, Optional: true, Variadic: true}},
		{v, []string{"a", "b", "c"}, Expectation{Positional: 3, Type: ArgString, Optional: true, Variadic: true}},
	}
	for _, c := range cases {
		if got := c.c.Expect(c.prior); got != c.want {
			t.Errorf("%s %v: got %+v, want %+v", c.c.Name, c.prior, got, c.want)
		}
	}
}

// exportHost serves a fixed active result and view.
type exportHost struct {
	testHost
	res  *exec.Result
	view exec.View
}

func (h *exportHost) ActiveResult() (*exec.Result, exec.View) { return h.res, h.view }

// evenView keeps rows whose id is even and sorts by id descending.
type evenView struct{ sorted bool }

func (v evenView) Keep(r exec.Row) bool {
	return len(r[0].(string))%2 == 0 || r[0].(string) == "2" || r[0].(string) == "4"
}
func (v evenView) Sorted() bool { return v.sorted }
func (v evenView) Order(rows []exec.Row) []exec.Row {
	out := append([]exec.Row(nil), rows...)
	sort.Slice(out, func(i, j int) bool { return out[i][0].(string) > out[j][0].(string) })
	return out
}

func newExportRuntime(t *testing.T, res *exec.Result, view exec.View) (*Runtime, *exportHost) {
	h := &exportHost{res: res, view: view}
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	rt := New(Env{Workspace: session.NewWorkspace(cfg, nil), Host: h})
	t.Cleanup(rt.Close)
	mustLoad(t, rt, `dbcc.command{ name = "x", args = { "string", "string?" }, run = function(ctx, args)
  ctx:message("n=" .. ctx:export(args[1], { force = args[2] == "force", progress = 2 }))
end }`)
	return rt, h
}

func TestExportPrimitive(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id", Numeric: true}, {Name: "name"}}
	rows := []exec.Row{{"1", "a"}, {"2", nil}, {"3", "c,d"}, {"4", "e"}}
	dir := t.TempDir()

	rt, h := newExportRuntime(t, exec.NewResult("r", cols, rows), nil)
	csvPath := filepath.Join(dir, "out.csv")
	if err := run(t, rt, "x", csvPath); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(csvPath)
	if string(b) != "id,name\n1,a\n2,\n3,\"c,d\"\n4,e\n" {
		t.Errorf("csv:\n%s", b)
	}
	if !strings.Contains(h.infos(), "n=4") || !strings.Contains(h.infos(), "2 rows…") {
		t.Errorf("messages: %s", h.infos())
	}
	// No overwrite without force.
	if err := run(t, rt, "x", csvPath); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("overwrite: %v", err)
	}
	if err := run(t, rt, "x", csvPath, "force"); err != nil {
		t.Errorf("force: %v", err)
	}
	// Unknown format.
	if err := run(t, rt, "x", filepath.Join(dir, "out.txt")); err == nil || !strings.Contains(err.Error(), ".csv or .json") {
		t.Errorf("format: %v", err)
	}

	// JSON with a local filter, unsorted.
	rt, _ = newExportRuntime(t, exec.NewResult("r", cols, rows), evenView{})
	jsonPath := filepath.Join(dir, "out.json")
	if err := run(t, rt, "x", jsonPath); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(jsonPath)
	if string(b) != "[\n  {\"id\": \"2\", \"name\": null},\n  {\"id\": \"4\", \"name\": \"e\"}\n]\n" {
		t.Errorf("json:\n%s", b)
	}
	// Filter and sort.
	rt, _ = newExportRuntime(t, exec.NewResult("r", cols, rows), evenView{sorted: true})
	p := filepath.Join(dir, "sorted.csv")
	if err := run(t, rt, "x", p); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if string(b) != "id,name\n4,e\n2,\n" {
		t.Errorf("sorted csv:\n%s", b)
	}
	// No active result.
	rt, _ = newExportRuntime(t, nil, nil)
	if err := run(t, rt, "x", filepath.Join(dir, "none.csv")); !errors.Is(err, ErrNoResult) {
		t.Errorf("no result: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "none.csv")); err == nil {
		t.Errorf("file created without result")
	}
}

func TestExportCancelled(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id"}}
	rt, _ := newExportRuntime(t, exec.NewResult("r", cols, []exec.Row{{"1"}}), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := rt.Command("x")
	p := filepath.Join(t.TempDir(), "c.csv")
	if err := rt.Run(ctx, c, []string{p}); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	if _, err := os.Stat(p); err == nil {
		t.Errorf("partial file left behind")
	}
}

func TestHistoryPrimitive(t *testing.T) {
	hist, err := history.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hist.Record(exec.Message{Stmt: "SELECT 1"}, "p", "/p/db")
	hist.Record(exec.Message{Stmt: "SELECT x", Err: errors.New("bad")}, "p", "/p/db")
	h := &testHost{}
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	rt := New(Env{Workspace: session.NewWorkspace(cfg, nil), Host: h, History: hist})
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "h", run = function(ctx)
  for _, e in ipairs(ctx:history(5)) do print(e.stmt, e.ok, e.outcome, e.profile, e.path, type(e.duration_ms), #e.time) end
end }`)
	if err := run(t, rt, "h"); err != nil {
		t.Fatal(err)
	}
	if got := h.infos(); got != "SELECT 1\ttrue\tok\tp\t/p/db\tnumber\t19\nSELECT x\tfalse\tbad\tp\t/p/db\tnumber\t19" {
		t.Errorf("history: %q", got)
	}
}

func TestFmtSizeCount(t *testing.T) {
	rt, h := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "f", run = function(ctx)
  print(dbcc.fmt.size(0), dbcc.fmt.size(1023), dbcc.fmt.size(16384), dbcc.fmt.size(1572864), dbcc.fmt.size(nil), dbcc.fmt.count(1234567))
end }`)
	if err := run(t, rt, "f"); err != nil {
		t.Fatal(err)
	}
	if got := h.infos(); got != "0 B\t1023 B\t16.0 KiB\t1.5 MiB\t\t1,234,567" {
		t.Errorf("got %q", got)
	}
}
