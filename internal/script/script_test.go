package script

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func mustLoad(t *testing.T, rt *Runtime, src string) {
	t.Helper()
	if _, err := rt.Load("test.lua", src, false); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, rt *Runtime, name string, args ...string) error {
	t.Helper()
	c, ok := rt.Command(name)
	if !ok {
		t.Fatalf("no command %s", name)
	}
	return rt.Run(context.Background(), c, args)
}

func TestSandbox(t *testing.T) {
	rt, h := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `
dbcc.command{ name = "probe", run = function(ctx, args)
  local names = { "os", "io", "debug", "package", "dofile", "loadfile", "require", "module", "newproxy", "coroutine", "channel" }
  for _, n in ipairs(names) do print(n .. "=" .. type(_G[n])) end
  print("string=" .. type(string.format), "table=" .. type(table.concat), "math=" .. type(math.floor))
  print("load=" .. type(loadstring("return 1")))
end }`)
	if err := run(t, rt, "probe"); err != nil {
		t.Fatal(err)
	}
	out := h.infos()
	for _, n := range []string{"os", "io", "debug", "package", "dofile", "loadfile", "require", "module", "newproxy", "coroutine", "channel"} {
		if !strings.Contains(out, n+"=nil") {
			t.Errorf("%s is reachable in the sandbox:\n%s", n, out)
		}
	}
	for _, want := range []string{"string=function", "table=function", "math=function", "load=function"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}
}

func TestDbccTableIsReadOnly(t *testing.T) {
	rt, _ := newTestRuntime()
	defer rt.Close()
	if _, err := rt.Load("evil.lua", `dbcc.command = nil`, false); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("want read-only error, got %v", err)
	}
	// Globals written by one script stay in that script's environment.
	mustLoad(t, rt, `string = nil; x = 1
dbcc.command{ name = "a", run = function() end }`)
	mustLoad(t, rt, `dbcc.command{ name = "b", run = function(ctx) print(type(string), type(x)) end }`)
	rt2h := rt.env.Host.(*testHost)
	if err := run(t, rt, "b"); err != nil {
		t.Fatal(err)
	}
	if got := rt2h.infos(); got != "table\tnil" {
		t.Fatalf("globals leaked between scripts: %q", got)
	}
}

func TestRegistrationValidation(t *testing.T) {
	cases := map[string]string{
		`dbcc.command{ name = "x" }`:                                                    `"run" must be a function`,
		`dbcc.command{ run = function() end }`:                                          `"name" is required`,
		`dbcc.command{ name = "dbcc", run = function() end }`:                           `reserved`,
		`dbcc.command{ name = "Bad Name", run = function() end }`:                       `invalid command name`,
		`dbcc.command{ name = "x", writes = true, run = function() end }`:               `unknown field`,
		`dbcc.command{ name = "x", args = {"table"}, run = function() end }`:            `unknown type`,
		`dbcc.command{ name = "x", args = {"int?", "object"}, run = function() end }`:   `required argument cannot follow`,
		`dbcc.command{ name = "x", args = {"string...", "int"}, run = function() end }`: `variadic`,
		`dbcc.command{ name = "x", write = "yes", run = function() end }`:               `boolean`,
		`dbcc.command{ name = "x", run = function() end } error("boom")`:                `boom`,
		`dbcc.command{ name = "x", run = function() end`:                                `test.lua`,
	}
	for src, want := range cases {
		rt, _ := newTestRuntime()
		_, err := rt.Load("test.lua", src, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s\n  want error containing %q, got %v", src, want, err)
		}
		if _, ok := rt.Command("x"); ok {
			t.Errorf("%s: failed script registered a command", src)
		}
		rt.Close()
	}
}

func TestCommandOutsideLoad(t *testing.T) {
	rt, _ := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "late", run = function() dbcc.command{ name = "y", run = function() end } end }`)
	err := run(t, rt, "late")
	if err == nil || !strings.Contains(err.Error(), "while a command script loads") {
		t.Fatalf("got %v", err)
	}
}

func TestArity(t *testing.T) {
	rt, _ := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `
dbcc.command{ name = "h", usage = "h <table|view> [n]", args = { "object", "int?" }, run = function() end }
dbcc.command{ name = "v", args = { "string", "string..." }, run = function() end }
dbcc.command{ name = "z", run = function() end }`)
	cases := []struct {
		cmd  string
		args []string
		ok   bool
	}{
		{"h", nil, false},
		{"h", []string{"t"}, true},
		{"h", []string{"t", "5"}, true},
		{"h", []string{"t", "-3"}, true},
		{"h", []string{"t", "x"}, false},
		{"h", []string{"t", "5", "6"}, false},
		{"v", nil, false},
		{"v", []string{"a"}, true},
		{"v", []string{"a", "b", "c", "d"}, true},
		{"z", nil, true},
		{"z", []string{"a"}, false},
	}
	for _, c := range cases {
		cmd, _ := rt.Command(c.cmd)
		err := cmd.CheckArgs(c.args)
		var ue *UsageError
		if c.ok != (err == nil) || (err != nil && !errors.As(err, &ue)) {
			t.Errorf("%s %v: got %v", c.cmd, c.args, err)
		}
	}
	h, _ := rt.Command("h")
	if err := rt.Run(context.Background(), h, nil); err == nil || !strings.Contains(err.Error(), "usage: h <table|view> [n]") {
		t.Errorf("Run without args: %v", err)
	}
	if v, _ := rt.Command("v"); v.Usage != "v <string> [string ...]" {
		t.Errorf("default usage %q", v.Usage)
	}
}

func TestFmtTable(t *testing.T) {
	rt, h := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "f", run = function(ctx)
  ctx:text(dbcc.fmt.table({ "id", "name" }, { { 1, "alpha" }, { 22, nil }, { 3, "c" } }))
end }`)
	if err := run(t, rt, "f"); err != nil {
		t.Fatal(err)
	}
	want := "id | name\n---+------\n 1 | alpha\n22 | NULL\n 3 | c\n"
	if len(h.texts) != 1 || h.texts[0] != want {
		t.Fatalf("got\n%q\nwant\n%q", h.texts, want)
	}
}

func TestErrorsCarryCommandName(t *testing.T) {
	rt, _ := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `
dbcc.command{ name = "boom", run = function() error("kaputt") end }
dbcc.command{ name = "meta", run = function(ctx) ctx:meta("t") end }
dbcc.command{ name = "noconn", run = function(ctx) ctx:query("SELECT 1") end }
dbcc.command{ name = "dot", run = function(ctx) ctx.quote("x") end }
dbcc.command{ name = "pick", run = function(ctx) ctx:pick({ "a" }) end }`)
	cases := map[string]string{
		"boom":   "boom: test.lua:2: kaputt",
		"meta":   "meta: not connected",
		"noconn": "noconn: not connected",
		"dot":    "call it with a colon",
		"pick":   "pick: ctx:pick: not available without UI",
	}
	for name, want := range cases {
		err := run(t, rt, name)
		var ce *CommandError
		if !errors.As(err, &ce) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", name, want, err)
		}
	}
	if err := run(t, rt, "meta"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Go error not preserved: %v", err)
	}
	if err := run(t, rt, "pick"); !errors.Is(err, ErrNoUI) {
		t.Errorf("Go error not preserved: %v", err)
	}
}

func TestPathAtRootAndRows(t *testing.T) {
	rt, h := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "p", run = function(ctx)
  assert(ctx.path.level == "root" and ctx.path.conn == nil and ctx.dialect == nil and ctx.profile == nil)
  ctx:text(tostring(ctx.path))
  ctx:rows({ "n", "s" }, { { 1, "a" }, { 2.5, nil } }, { title = "T" })
  local q = dbcc.commands()
  ctx:message(#q .. " " .. q[1].name)
end }`)
	if err := run(t, rt, "p"); err != nil {
		t.Fatal(err)
	}
	if h.texts[0] != "/" {
		t.Errorf("path %q", h.texts[0])
	}
	r := h.results[0]
	if r.Title != "T" || !r.Columns[0].Numeric || r.Columns[1].Numeric || r.Rows()[1][1] != nil || r.Rows()[1][0] != "2.5" {
		t.Errorf("rows result: %+v %v", r.Columns, r.Rows())
	}
	if h.infos() != "1 p" {
		t.Errorf("commands: %q", h.infos())
	}
}

func TestSplitQualified(t *testing.T) {
	cases := map[string][]string{
		"t":        {"t"},
		"db.t":     {"db", "t"},
		"`a.b`.c":  {"a.b", "c"},
		`"x""y".z`: {`x"y`, "z"},
		"[s].[t]":  {"s", "t"},
		"a.b.c":    {"a", "b", "c"},
	}
	for in, want := range cases {
		got, err := splitQualified(in)
		if err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: got %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"a.", ".a", "`a", "a..b"} {
		if _, err := splitQualified(bad); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func TestLoadTimeout(t *testing.T) {
	old := LoadTimeout
	LoadTimeout = 100_000_000 // 100ms
	defer func() { LoadTimeout = old }()
	rt, _ := newTestRuntime()
	defer rt.Close()
	if _, err := rt.Load("loop.lua", "while true do end", false); err == nil {
		t.Fatal("endless loop at load did not fail")
	}
}
