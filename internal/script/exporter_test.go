package script

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/export"
	"dbcc/internal/script/commands"
	"dbcc/internal/session"
)

func TestExporterDeclaration(t *testing.T) {
	w := `write = function() end`
	cases := map[string]string{
		`dbcc.exporter{ name = "x" }`:                                                                                             `"write" must be a function`,
		`dbcc.exporter{ ` + w + ` }`:                                                                                              `"name" is required`,
		`dbcc.exporter{ name = "Bad", ` + w + ` }`:                                                                                `invalid exporter name`,
		`dbcc.exporter{ name = "x", writer = 1, ` + w + ` }`:                                                                      `unknown field(s) [writer]`,
		`dbcc.exporter{ name = "x", title = 1, ` + w + ` }`:                                                                       `"title" must be a string`,
		`dbcc.exporter{ name = "x", extensions = "csv", ` + w + ` }`:                                                              `"extensions" must be a list`,
		`dbcc.exporter{ name = "x", extensions = { "a/b" }, ` + w + ` }`:                                                          `extensions must be a list of file extensions`,
		`dbcc.exporter{ name = "x", options = "a", ` + w + ` }`:                                                                   `field "options" must be a list`,
		`dbcc.exporter{ name = "x", options = { "a" }, ` + w + ` }`:                                                               `options[1] must be a table`,
		`dbcc.exporter{ name = "x", options = { { type = "bool" } }, ` + w + ` }`:                                                 `options[1]: field "name" is required`,
		`dbcc.exporter{ name = "x", options = { { name = "A", type = "bool" } }, ` + w + ` }`:                                     `invalid option name "A"`,
		`dbcc.exporter{ name = "x", options = { { name = "a" } }, ` + w + ` }`:                                                    `a: field "type" is required`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "float" } }, ` + w + ` }`:                                    `a: unknown type "float" (string, int, bool, choice)`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "bool", dflt = 1 } }, ` + w + ` }`:                           `unknown field(s) [dflt]`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "bool", default = "yes" } }, ` + w + ` }`:                    `a: default "yes" does not fit type bool`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "int", default = 1.5 } }, ` + w + ` }`:                       `a: default 1.5 does not fit type int`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "string", default = 1 } }, ` + w + ` }`:                      `a: default 1 does not fit type string`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "choice" } }, ` + w + ` }`:                                   `a: a choice needs choices`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "choice", choices = {} } }, ` + w + ` }`:                     `a: choices must not be empty`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "choice", choices = { 1 } } }, ` + w + ` }`:                  `a: choices[1] must be a string or {value=, label=}`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "choice", choices = { "p" }, default = "q" } }, ` + w + ` }`: `a: default "q" does not fit type choice`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "bool", choices = { "p" } } }, ` + w + ` }`:                  `choices are only allowed for type "choice"`,
		`dbcc.exporter{ name = "x", options = { { name = "a", type = "bool" }, { name = "a", type = "int" } }, ` + w + ` }`:       `options[2]: duplicate option "a"`,
		`dbcc.exporter{ name = "x", ` + w + ` } error("boom")`:                                                                    `boom`,
	}
	for src, want := range cases {
		rt, _ := newTestRuntime()
		_, err := rt.Load("test.lua", src, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s\n  want error containing %q, got %v", src, want, err)
		}
		if _, ok := rt.Exporter("x"); ok {
			t.Errorf("%s: failed script registered an exporter", src)
		}
		rt.Close()
	}
	rt, _ := newTestRuntime()
	defer rt.Close()
	if err := run(t, rtWith(t, rt, `dbcc.command{ name = "late", run = function() dbcc.exporter{ name = "y", write = function() end } end }`), "late"); err == nil ||
		!strings.Contains(err.Error(), "while a command script loads") {
		t.Errorf("exporter outside load: %v", err)
	}
}

func rtWith(t *testing.T, rt *Runtime, src string) *Runtime {
	t.Helper()
	mustLoad(t, rt, src)
	return rt
}

func TestExporterOverrideAndList(t *testing.T) {
	rt, h := newTestRuntime()
	defer rt.Close()
	if _, err := rt.Load("embedded:exporter_md.lua", `dbcc.exporter{ name = "md", title = "Markdown", extensions = { ".md" },
  options = { { name = "align", label = "Align", type = "choice", choices = { { value = "l", label = "left" }, "r" } },
              { name = "width", type = "int", default = 4, help = "cells" } },
  write = function() end }`, true); err != nil {
		t.Fatal(err)
	}
	// A broken user script leaves the default active.
	if _, err := rt.Load("/u/broken.lua", `dbcc.exporter{ name = "md", title = "mine", write = function() end } error("x")`, false); err == nil {
		t.Fatal("broken script loaded")
	}
	if e, _ := rt.Exporter("md"); e.Title != "Markdown" || e.Replaces || !e.Default {
		t.Fatalf("after broken override: %+v", e)
	}
	// A user exporter of the same name replaces it.
	names, err := rt.Load("/u/md.lua", `dbcc.exporter{ name = "md", title = "Mine", write = function() end }
dbcc.command{ name = "l", run = function(ctx)
  for _, e in ipairs(dbcc.exporters()) do
    print(e.name, e.title, e.source, e.default, #e.extensions, #e.options)
  end
end }`, false)
	if err != nil || strings.Join(names, ",") != "l,exporter:md" {
		t.Fatalf("load: %v %v", names, err)
	}
	if e, _ := rt.Exporter("md"); e.Title != "Mine" || !e.Replaces || e.Default || e.Source != "/u/md.lua" {
		t.Fatalf("override: %+v", e)
	}
	if err := run(t, rt, "l"); err != nil {
		t.Fatal(err)
	}
	if got := h.infos(); got != "md\tMine\t/u/md.lua\tfalse\t0\t0" {
		t.Errorf("exporters: %q", got)
	}

	// The option table of dbcc.exporters().
	rt2, h2 := newTestRuntime()
	defer rt2.Close()
	mustLoad(t, rt2, `dbcc.exporter{ name = "md", extensions = { ".md", "markdown" },
  options = { { name = "align", label = "Align", type = "choice", choices = { { value = "l", label = "left" }, "r" } },
              { name = "width", type = "int", default = 4, help = "cells" }, { name = "on", type = "bool" }, { name = "s", type = "string" } },
  write = function() end }
dbcc.command{ name = "l", run = function(ctx)
  local e = dbcc.exporters()[1]
  print(e.title, table.concat(e.extensions, ","))
  for _, o in ipairs(e.options) do
    local ch = {}
    for _, c in ipairs(o.choices or {}) do ch[#ch + 1] = c.value .. "=" .. c.label end
    print(o.name, o.label, o.type, tostring(o.default), o.help, table.concat(ch, ","))
  end
end }`)
	if err := run(t, rt2, "l"); err != nil {
		t.Fatal(err)
	}
	want := "md\tmd,markdown\nalign\tAlign\tchoice\tl\t\tl=left,r=r\nwidth\twidth\tint\t4\tcells\t\non\ton\tbool\tfalse\t\t\ns\ts\tstring\t\t\t"
	if got := h2.infos(); got != want {
		t.Errorf("options:\n%q\nwant\n%q", got, want)
	}
}

func TestOptionConvert(t *testing.T) {
	ch := OptionSpec{Name: "d", Type: OptChoice, Choices: []Choice{{",", "comma  ,"}, {"\t", "tab"}, {"1", "one"}}}
	cases := []struct {
		o    OptionSpec
		in   any
		want any // nil: error
	}{
		{OptionSpec{Type: OptBool}, "true", true},
		{OptionSpec{Type: OptBool}, "YES", true},
		{OptionSpec{Type: OptBool}, "0", false},
		{OptionSpec{Type: OptBool}, "no", false},
		{OptionSpec{Type: OptBool}, false, false},
		{OptionSpec{Type: OptBool}, "maybe", nil},
		{OptionSpec{Type: OptBool}, float64(1), nil},
		{OptionSpec{Type: OptInt}, "-12", int64(-12)},
		{OptionSpec{Type: OptInt}, float64(7), int64(7)},
		{OptionSpec{Type: OptInt}, float64(7.5), nil},
		{OptionSpec{Type: OptInt}, "x", nil},
		{OptionSpec{Type: OptString}, "a b", "a b"},
		{OptionSpec{Type: OptString}, float64(3), "3"},
		{OptionSpec{Type: OptString}, true, nil},
		{ch, ",", ","},
		{ch, "comma", ","},
		{ch, "TAB", "\t"},
		{ch, float64(1), "1"},
		{ch, ";", nil},
	}
	for _, c := range cases {
		got, err := c.o.Convert(c.in)
		if c.want == nil {
			if err == nil {
				t.Errorf("%s %#v: want error, got %#v", c.o.Type, c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s %#v: got %#v, %v; want %#v", c.o.Type, c.in, got, err, c.want)
		}
	}
	if _, err := ch.Convert(";"); err == nil || !strings.Contains(err.Error(), `";" is not one of "," (comma), "\t" (tab), "1" (one)`) {
		t.Errorf("choice error: %v", err)
	}
	specs := []OptionSpec{{Name: "a", Type: OptBool, Default: true}, {Name: "b", Type: OptInt, Default: int64(2)}}
	got, err := ResolveOptions(specs, map[string]any{"b": "5"})
	if err != nil || got["a"] != true || got["b"] != int64(5) {
		t.Errorf("resolve: %v %v", got, err)
	}
	if _, err := ResolveOptions(specs, map[string]any{"c": "1"}); err == nil || err.Error() != `unknown option "c" (options: a, b)` {
		t.Errorf("unknown: %v", err)
	}
	if _, err := ResolveOptions(specs, map[string]any{"b": "x"}); err == nil || err.Error() != `option b: want an integer, got "x"` {
		t.Errorf("bad value: %v", err)
	}
	if _, err := ResolveOptions(nil, map[string]any{"c": "1"}); err == nil || !strings.Contains(err.Error(), "there are no options") {
		t.Errorf("no options: %v", err)
	}
}

func TestFmtCSV(t *testing.T) {
	rt, h := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "f", args = { "string" }, run = function(ctx, args)
  local f = loadstring("return " .. args[1])
  local ok, s = pcall(dbcc.fmt.csv, f())
  print(ok and ("[" .. s .. "]") or ("error: " .. tostring(s)))
end }`)
	cases := []struct{ call, want string }{
		{`{ "a", "b,c", 'say "hi"', "x\ny", " lead", "trail ", "", nil, 3, 2.5 }, 10`,
			`[a,"b,c","say ""hi""","x` + "\n" + `y"," lead",trail ,,,3,2.5]`},
		{`{ "\\.", "a\rb", "\tx", "é" }, 4`, `["\.","a` + "\r" + `b","` + "\t" + `x",é]`},
		{`{ "a", nil, "c" }, 3, { null = "NULL" }`, `[a,NULL,c]`},
		{`{ "a", nil }, 2, { quoting = "all", null = "\\N" }`, `["a",\N]`},
		{`{ "a", 1, "", nil }, 4, { quoting = "all" }`, `["a","1","",]`},
		{`{ "a", 1, "2" }, 3, { quoting = "nonnumeric" }`, `["a",1,"2"]`},
		{`{ "a", 1, "2" }, 3, { quoting = "nonnumeric", numeric = { false, false, true } }`, `["a",1,2]`},
		{`{ "a;b", "c" }, 2, { delimiter = ";" }`, `["a;b";c]`},
		{`{ "a,b", "it's" }, 2, { quote = "'" }`, `['a,b','it''s']`},
		{`{ "a|b" }, 1, { delimiter = "|", quoting = "none" }`, `dbcc.fmt.csv: field 1 contains the delimiter`},
		{`{ " a", "b" }, 2, { quoting = "none" }`, `[ a,b]`},
		{`{ "a", "b" }, 1`, `[a]`},
		{`{ "x" }, 3`, `[x,,]`},
		{`{ "a" }, 1, { delimiter = ";;" }`, `opts.delimiter must be exactly one character`},
		{`{ "a" }, 1, { delimiter = "'", quote = "'" }`, `must differ`},
		{`{ "a" }, 1, { quoting = "some" }`, `opts.quoting must be minimal, all, nonnumeric or none`},
		{`{ {} }, 1`, `field 1 is a table`},
		{`{ true }, 1`, `[true]`},
		{`{ "x" }, 1, { delimiter = "\t" }`, `[x]`},
		{`{ "a b", "c" }, 2, { delimiter = "\t" }`, "[a b\tc]"},
		{`{ "ä;ö" }, 1, { delimiter = "ö" }`, `["ä;ö"]`},
		// numeric columns by type name can hold the delimiter (arrays, money)
		{`{ "{1,2,3}", "7", "[1,10)", 'x"y' }, 4, { quoting = "nonnumeric", numeric = { true, true, true, true } }`, `["{1,2,3}",7,"[1,10)","x""y"]`},
	}
	for _, c := range cases {
		h.messages = nil
		if err := run(t, rt, "f", c.call); err != nil {
			t.Fatal(err)
		}
		if got := h.infos(); !strings.Contains(got, c.want) {
			t.Errorf("dbcc.fmt.csv(%s)\n got  %q\n want %q", c.call, got, c.want)
		}
	}
}

func TestFmtJSONAndCell(t *testing.T) {
	rt, h := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "f", run = function(ctx)
  print(dbcc.fmt.json("a\"b\n<&>é"), dbcc.fmt.json(nil), dbcc.fmt.json(true), dbcc.fmt.json(3), dbcc.fmt.json(-2.5), dbcc.fmt.json(1e20))
  print(pcall(dbcc.fmt.json, {}))
  print("[" .. dbcc.fmt.cell(nil) .. "]", dbcc.fmt.cell(3), dbcc.fmt.cell(3.25), dbcc.fmt.cell(1e15), dbcc.fmt.cell(false), dbcc.fmt.cell("x"))
end }`)
	if err := run(t, rt, "f"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(h.infos(), "\n")
	if lines[0] != `"a\"b\n\u003c\u0026\u003eé"`+"\tnull\ttrue\t3\t-2.5\t100000000000000000000" {
		t.Errorf("json: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "false\t") || !strings.Contains(lines[1], "only strings, numbers, booleans and nil") {
		t.Errorf("json table: %q", lines[1])
	}
	if lines[2] != "[]\t3\t3.25\t1000000000000000\tfalse\tx" {
		t.Errorf("cell: %q", lines[2])
	}
}

// exportFixture is a result with values that exercise CSV quoting and
// JSON escaping.
func exportFixture() *exec.Result {
	cols := []exec.ColumnInfo{{Name: "id", DBType: "INT", Numeric: true}, {Name: "name", DBType: "VARCHAR"},
		{Name: "note, \"quoted\"", DBType: "TEXT"}, {Name: "bin", DBType: "BLOB", Binary: true}}
	rows := []exec.Row{
		{"1", "plain", "a,b", exec.Binary("xyz")},
		{"2", nil, `say "hi"`, nil},
		{"3", " lead", "line\nbreak", exec.Binary{}},
		{"4", "", `\.`, nil},
		{"5", "trail ", "<tag>&é", nil},
		{"6", "1.50", "crlf\r\nend", nil},
	}
	return exec.NewResult("my result", cols, rows)
}

// oldExport writes res with the writers export used before A28.
func oldExport(t *testing.T, format string, res *exec.Result) string {
	t.Helper()
	var b bytes.Buffer
	w, err := export.New(format, &b, nil, exec.CellText)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Name
	}
	w.Header(names)
	for _, r := range res.Rows() {
		w.Row(r)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// newExportWithRuntime loads the embedded exporters and a command xw whose
// run body is body (args[1] is the target file).
func newExportWithRuntime(t *testing.T, res *exec.Result, view exec.View, body string) (*Runtime, *exportHost) {
	t.Helper()
	h := &exportHost{res: res, view: view}
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	rt := New(Env{Workspace: session.NewWorkspace(cfg, nil), Host: h})
	t.Cleanup(rt.Close)
	for _, n := range []string{"exporter_csv.lua", "exporter_json.lua"} {
		b, err := commands.FS.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rt.Load("embedded:"+n, string(b), true); err != nil {
			t.Fatal(err)
		}
	}
	mustLoad(t, rt, `dbcc.command{ name = "xw", args = { "file" }, run = function(ctx, args)
  `+body+`
end }`)
	return rt, h
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExportWithDefaultsMatchOldWriters(t *testing.T) {
	dir := t.TempDir()
	for _, format := range []string{"csv", "json"} {
		rt, h := newExportWithRuntime(t, exportFixture(), nil,
			`ctx:message("n=" .. ctx:export_with("`+format+`", args[1], { progress = 0 }))`)
		p := filepath.Join(dir, "out."+format)
		if err := run(t, rt, "xw", p); err != nil {
			t.Fatal(err)
		}
		if got, want := readFile(t, p), oldExport(t, format, exportFixture()); got != want {
			t.Errorf("%s differs from the old writer:\n got  %q\n want %q", format, got, want)
		}
		if h.infos() != "n=6" {
			t.Errorf("%s: messages %q", format, h.infos())
		}
	}
	// No rows at all.
	empty := exec.NewResult("e", []exec.ColumnInfo{{Name: "a"}}, nil)
	for _, format := range []string{"csv", "json"} {
		rt, _ := newExportWithRuntime(t, empty, nil, `ctx:export_with("`+format+`", args[1])`)
		p := filepath.Join(dir, "empty."+format)
		if err := run(t, rt, "xw", p); err != nil {
			t.Fatal(err)
		}
		if got, want := readFile(t, p), oldExport(t, format, empty); got != want {
			t.Errorf("empty %s: got %q, want %q", format, got, want)
		}
	}
}

func TestExportWithOptions(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id", Numeric: true}, {Name: "name"}}
	rows := []exec.Row{{"1", "a;b"}, {"2", nil}, {"3", "it's"}}
	cases := []struct{ opts, want string }{
		{`delimiter = ";"`, "id;name\n1;\"a;b\"\n2;\n3;it's\n"},
		{`delimiter = "tab"`, "id\tname\n1\ta;b\n2\t\n3\tit's\n"},
		{`delimiter = "\t"`, "id\tname\n1\ta;b\n2\t\n3\tit's\n"},
		{`delimiter = "|", header = "false"`, "1|a;b\n2|\n3|it's\n"},
		{`header = false`, "1,a;b\n2,\n3,it's\n"},
		{`quoting = "all"`, "\"id\",\"name\"\n\"1\",\"a;b\"\n\"2\",\n\"3\",\"it's\"\n"},
		{`quoting = "nonnumeric"`, "\"id\",\"name\"\n1,\"a;b\"\n2,\n3,\"it's\"\n"},
		{`quoting = "all", quote = "'"`, "'id','name'\n'1','a;b'\n'2',\n'3','it''s'\n"},
		{`quote = "single"`, "id,name\n1,a;b\n2,\n3,'it''s'\n"},
		{`null = "NULL"`, "id,name\n1,a;b\n2,NULL\n3,it's\n"},
		{`line_ending = "crlf"`, "id,name\r\n1,a;b\r\n2,\r\n3,it's\r\n"},
		{`bom = "yes"`, "\ufeffid,name\n1,a;b\n2,\n3,it's\n"},
		{`quoting = "none"`, "id,name\n1,a;b\n2,\n3,it's\n"},
	}
	dir := t.TempDir()
	for i, c := range cases {
		rt, _ := newExportWithRuntime(t, exec.NewResult("r", cols, rows), nil,
			`ctx:export_with("csv", args[1], { options = { `+c.opts+` } })`)
		p := filepath.Join(dir, "o"+string(rune('a'+i))+".csv")
		if err := run(t, rt, "xw", p); err != nil {
			t.Errorf("%s: %v", c.opts, err)
			continue
		}
		if got := readFile(t, p); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.opts, got, c.want)
		}
	}
	// Compact JSON.
	rt, _ := newExportWithRuntime(t, exec.NewResult("r", cols, rows), nil, `ctx:export_with("json", args[1], { options = { pretty = false } })`)
	p := filepath.Join(dir, "c.json")
	if err := run(t, rt, "xw", p); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); got != `[{"id":"1","name":"a;b"},{"id":"2","name":null},{"id":"3","name":"it's"}]`+"\n" {
		t.Errorf("compact json: %q", got)
	}
	// quoting = none fails on a field that needs quotes; no file is left.
	rt, _ = newExportWithRuntime(t, exec.NewResult("r", cols, rows), nil, `ctx:export_with("csv", args[1], { options = { quoting = "none", delimiter = ";" } })`)
	p = filepath.Join(dir, "none.csv")
	if err := run(t, rt, "xw", p); err == nil || !strings.Contains(err.Error(), "field 2 contains the delimiter") {
		t.Errorf("quoting none: %v", err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("half-written file left behind")
	}
}

func TestExportWithErrors(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id"}}
	rows := []exec.Row{{"1"}, {"2"}, {"3"}}
	dir := t.TempDir()
	cases := []struct{ body, want string }{
		{`ctx:export_with("xlsx", args[1])`, `unknown export type "xlsx" (types: csv, json)`},
		{`ctx:export_with("csv", args[1], { options = { sep = ";" } })`, `export type csv: unknown option "sep" (options: bom, delimiter, header, line_ending, null, quote, quoting)`},
		{`ctx:export_with("csv", args[1], { options = { header = "maybe" } })`, `export type csv: option header: want true or false (also yes/no, 1/0), got "maybe"`},
		{`ctx:export_with("csv", args[1], { options = { delimiter = "#" } })`, `export type csv: option delimiter: "#" is not one of "," (comma), ";" (semicolon), "\t" (tab), "|" (pipe)`},
		{`ctx:export_with("csv", args[1], { options = { header = {} } })`, `opts.options.header must be a string, number or boolean`},
		{`ctx:export_with("csv", args[1], { options = "x" })`, `opts.options must be a table`},
	}
	for _, c := range cases {
		rt, _ := newExportWithRuntime(t, exec.NewResult("r", cols, rows), nil, c.body)
		p := filepath.Join(dir, "e.csv")
		if err := run(t, rt, "xw", p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s:\n got  %v\n want %q", c.body, err, c.want)
		}
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s: file created", c.body)
		}
	}
	// No active result.
	rt, _ := newExportWithRuntime(t, nil, nil, `ctx:export_with("csv", args[1])`)
	if err := run(t, rt, "xw", filepath.Join(dir, "n.csv")); !errors.Is(err, ErrNoResult) {
		t.Errorf("no result: %v", err)
	}
	// Overwrite protection and force.
	p := filepath.Join(dir, "exists.csv")
	os.WriteFile(p, []byte("old"), 0o600)
	rt, _ = newExportWithRuntime(t, exec.NewResult("r", cols, rows), nil, `ctx:export_with("csv", args[1])`)
	if err := run(t, rt, "xw", p); err == nil || !strings.Contains(err.Error(), "exists.csv already exists; overwrite with export -f") {
		t.Errorf("overwrite: %v", err)
	}
	if readFile(t, p) != "old" {
		t.Error("existing file changed")
	}
	rt, _ = newExportWithRuntime(t, exec.NewResult("r", cols, rows), nil, `ctx:export_with("csv", args[1], { force = true })`)
	if err := run(t, rt, "xw", p); err != nil || readFile(t, p) != "id\n1\n2\n3\n" {
		t.Errorf("force: %v %q", err, readFile(t, p))
	}
}

// TestUserExporter: a pure-Lua exporter (Markdown table) written like a
// user plugin, with an error part-way that must remove the file.
func TestUserExporter(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id", DBType: "INT", Numeric: true}, {Name: "name", DBType: "TEXT"}}
	rows := []exec.Row{{"1", "a|b"}, {"2", nil}, {"3", "c"}, {"4", "d"}}
	view := evenView{sorted: true}
	rt, h := newExportWithRuntime(t, exec.NewResult("people", cols, rows), view, `ctx:message("n=" .. ctx:export_with("md", args[1], { options = { fail_at = tonumber(args[2] or "0") }, progress = 1 }))`)
	mustLoad(t, rt, `dbcc.command{ name = "xw2", args = { "file", "string" }, run = function(ctx, args)
  ctx:export_with("md", args[1], { options = { fail_at = tonumber(args[2]) } })
end }`)
	if _, err := rt.Load("/home/u/.config/dbcc/commands/markdown.lua", `
local function cell(v) return (dbcc.fmt.cell(v):gsub("|", "\\|")) end
dbcc.exporter{
  name = "md", title = "Markdown table", extensions = { "md" },
  options = { { name = "fail_at", type = "int", default = 0 } },
  write = function(ctx, src, out, opts)
    local head, rule = {}, {}
    for i, c in ipairs(src.columns) do
      head[i] = c.name .. (c.numeric and " (" .. c.type .. ")" or "")
      rule[i] = c.numeric and "--:" or "---"
    end
    out:write("# ", src.title, "\n\n| ", table.concat(head, " | "), " |\n|", table.concat(rule, "|"), "|\n")
    local k = 0
    src:each(function(row)
      k = k + 1
      if k == opts.fail_at then error("stop at " .. k) end
      local cells = {}
      for i = 1, #src.columns do cells[i] = cell(row[i]) end
      out:write("| ", table.concat(cells, " | "), " |\n")
    end)
    assert(out.path:sub(-3) == ".md")
  end,
}`, false); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "t.md")
	if err := run(t, rt, "xw", p); err != nil {
		t.Fatal(err)
	}
	// evenView keeps 2 and 4 and sorts descending.
	want := "# people\n\n| id (INT) | name |\n|--:|---|\n| 4 | d |\n| 2 |  |\n"
	if got := readFile(t, p); got != want {
		t.Errorf("markdown:\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(h.infos(), "export t.md: 1 rows…") || !strings.Contains(h.infos(), "n=2") {
		t.Errorf("messages: %q", h.infos())
	}
	// An error in the middle removes the half-written file.
	p2 := filepath.Join(dir, "fail.md")
	err := run(t, rt, "xw2", p2, "2")
	if err == nil || !strings.Contains(err.Error(), "markdown.lua:") || !strings.Contains(err.Error(), "stop at 2") {
		t.Errorf("error from the exporter: %v", err)
	}
	if _, serr := os.Stat(p2); serr == nil {
		t.Error("half-written file left behind")
	}
}

func TestExportWithCancelAndMisuse(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id"}}
	rt, _ := newExportWithRuntime(t, exec.NewResult("r", cols, []exec.Row{{"1"}}), nil, `ctx:export_with("csv", args[1])`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := rt.Command("xw")
	p := filepath.Join(t.TempDir(), "c.csv")
	if err := rt.Run(ctx, c, []string{p}); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	if _, err := os.Stat(p); err == nil {
		t.Errorf("partial file left behind")
	}

	// src and out are only valid during the call; each runs once.
	mustLoad(t, rt, `
local kept
dbcc.exporter{ name = "twice", write = function(ctx, src, out) src:each(function() end); src:each(function() end) end }
dbcc.exporter{ name = "dot", write = function(ctx, src, out) src.each(function() end) end }
dbcc.exporter{ name = "keep", write = function(ctx, src, out) kept = out end }
dbcc.exporter{ name = "nested", write = function(ctx, src, out) ctx:export_with("csv", out.path .. ".2") end }
dbcc.exporter{ name = "badwrite", write = function(ctx, src, out) out:write({}) end }
dbcc.command{ name = "m", args = { "string", "file" }, run = function(ctx, args)
  ctx:export_with(args[1], args[2])
  if kept then kept:write("late") end
end }`)
	dir := t.TempDir()
	for name, want := range map[string]string{
		"twice":    "src:each may be called only once",
		"dot":      "call it with a colon",
		"keep":     "out is only valid while the exporter's write function runs",
		"nested":   "ctx:export_with cannot run inside an exporter",
		"badwrite": "out:write takes strings and numbers",
	} {
		p := filepath.Join(dir, name+".txt")
		if err := run(t, rt, "m", name, p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", name, want, err)
		}
	}
	// keep: the export itself succeeded before the late write failed.
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Errorf("keep: %v", err)
	}
}

// formHost answers ctx:form and ctx:save_file.
type formHost struct {
	testHost
	gotTitle  string
	gotFields []OptionSpec
	gotValues map[string]any
	answer    map[string]any
	ok        bool
	saveOpts  SaveFileOptions
	path      string
}

func (h *formHost) Interactive() bool { return true }
func (h *formHost) Form(title string, fields []OptionSpec, values map[string]any) (map[string]any, bool, error) {
	h.gotTitle, h.gotFields, h.gotValues = title, fields, values
	return h.answer, h.ok, nil
}
func (h *formHost) SaveFile(o SaveFileOptions) (string, bool, error) {
	h.saveOpts = o
	return h.path, h.path != "", nil
}

func TestFormAndSaveFilePrimitives(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "a.csv")
	os.WriteFile(existing, nil, 0o600)
	h := &formHost{ok: true, answer: map[string]any{"n": int64(5), "b": true, "c": "y", "s": "text"}, path: existing}
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	rt := New(Env{Workspace: session.NewWorkspace(cfg, nil), Host: h})
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "f", run = function(ctx)
  local v = ctx:form({ { name = "n", type = "int", default = 1 }, { name = "b", type = "bool" },
                       { name = "c", type = "choice", choices = { "x", "y" } }, { name = "s", type = "string" } },
                     { title = "T", values = { n = "3", c = "y" } })
  print(ctx.ui, type(v.n), v.n, v.b, v.c, v.s)
  local p, exists = ctx:save_file{ title = "S", name = "a", dir = "~/x", extensions = { ".csv", "txt" } }
  print(p == nil, exists)
end }`)
	if err := run(t, rt, "f"); err != nil {
		t.Fatal(err)
	}
	if got := h.infos(); got != "true\tnumber\t5\ttrue\ty\ttext\nfalse\ttrue" {
		t.Errorf("got %q", got)
	}
	if h.gotTitle != "T" || len(h.gotFields) != 4 || h.gotValues["n"] != int64(3) || h.gotValues["b"] != false || h.gotValues["c"] != "y" {
		t.Errorf("form got %q %v %v", h.gotTitle, h.gotFields, h.gotValues)
	}
	if h.saveOpts.Title != "S" || h.saveOpts.Name != "a" || h.saveOpts.Dir != filepath.Join(config.HomeDir(), "x") ||
		strings.Join(h.saveOpts.Extensions, ",") != "csv,txt" {
		t.Errorf("save opts %+v", h.saveOpts)
	}
	// Cancel returns nil.
	h.ok, h.path = false, ""
	h.messages = nil
	mustLoad(t, rt, `dbcc.command{ name = "g", run = function(ctx)
  print(ctx:form({ { name = "n", type = "int" } }) == nil, ctx:save_file() == nil)
end }`)
	if err := run(t, rt, "g"); err != nil || h.infos() != "true\ttrue" {
		t.Errorf("cancel: %v %q", err, h.infos())
	}
	// Bad declarations and values.
	mustLoad(t, rt, `dbcc.command{ name = "bad", args = { "string" }, run = function(ctx, args)
  if args[1] == "1" then ctx:form({ { name = "n", type = "float" } }) end
  if args[1] == "2" then ctx:form({ { name = "n", type = "int" } }, { values = { n = "x" } }) end
  if args[1] == "3" then ctx:form({ { name = "n", type = "int" } }, { values = { m = 1 } }) end
end }`)
	for arg, want := range map[string]string{"1": `ctx:form: fields[1]: n: unknown type "float"`, "2": `ctx:form: option n: want an integer`, "3": `ctx:form: unknown option "m"`} {
		if err := run(t, rt, "bad", arg); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("bad %s: want %q, got %v", arg, want, err)
		}
	}
	// Headless hosts: ErrNoUI and ctx.ui = false.
	rt2, h2 := newTestRuntime()
	defer rt2.Close()
	mustLoad(t, rt2, `dbcc.command{ name = "f", args = { "string" }, run = function(ctx, args)
  print(ctx.ui)
  if args[1] == "form" then ctx:form({}) else ctx:save_file() end
end }`)
	for _, which := range []string{"form", "save"} {
		err := run(t, rt2, "f", which)
		if !errors.Is(err, ErrNoUI) {
			t.Errorf("%s headless: %v", which, err)
		}
	}
	if !strings.HasPrefix(h2.infos(), "false") {
		t.Errorf("ctx.ui headless: %q", h2.infos())
	}
}

func TestRepeatedFlag(t *testing.T) {
	rt, h := newTestRuntime()
	defer rt.Close()
	mustLoad(t, rt, `dbcc.command{ name = "x", args = { "string?" }, flags = { o = "string...", f = "bool" },
  run = function(ctx, args, flags)
    ctx:text(type(flags.o) .. ":" .. table.concat(flags.o or {}, "|") .. ":" .. tostring(args[1]))
  end }`)
	cases := []struct {
		args []string
		want string
	}{
		{nil, "nil::nil"},
		{[]string{"-o", "a=1"}, "table:a=1:nil"},
		{[]string{"-o", "a=1", "-f", "-o=b=2", "--o", "c", "file"}, "table:a=1|b=2|c:file"},
	}
	for _, c := range cases {
		h.texts = nil
		if err := run(t, rt, "x", c.args...); err != nil || len(h.texts) != 1 || h.texts[0] != c.want {
			t.Errorf("%v: got %q, %v; want %q", c.args, h.texts, err, c.want)
		}
	}
	x, _ := rt.Command("x")
	if x.Usage != "x [-f] [-o string]... [string]" {
		t.Errorf("usage %q", x.Usage)
	}
	if f, _ := x.Flag("o"); !f.Repeated || f.Type != "string" {
		t.Errorf("flag %+v", f)
	}
	var ue *UsageError
	if err := run(t, rt, "x", "-o"); !errors.As(err, &ue) {
		t.Errorf("-o without value: %v", err)
	}
	for src, want := range map[string]string{
		`dbcc.command{ name = "y", flags = { o = "bool..." }, run = function() end }`:   `a bool flag cannot be repeated`,
		`dbcc.command{ name = "y", flags = { o = "number..." }, run = function() end }`: `unknown type "number..."`,
	} {
		rt2, _ := newTestRuntime()
		if _, err := rt2.Load("t.lua", src, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", src, want, err)
		}
		rt2.Close()
	}
	// flag_values = "exporters" marks a dynamic value source.
	mustLoad(t, rt, `dbcc.command{ name = "z", flags = { t = "string" }, flag_values = { t = "exporters" }, run = function() end }`)
	z, _ := rt.Command("z")
	if f, _ := z.Flag("t"); f.ValuesFrom != "exporters" || len(f.Values) != 0 {
		t.Errorf("flag_values source: %+v", f)
	}
}

// TestCSVNonnumericRoundTrip: values of columns that exec reports as
// numeric by their type name (PostgreSQL arrays, ranges, money) can hold
// the delimiter; "nonnumeric" must still produce valid CSV.
func TestCSVNonnumericRoundTrip(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "arr", DBType: "_INT4", Numeric: true}, {Name: "rng", DBType: "INT4RANGE", Numeric: true},
		{Name: "m", DBType: "MONEY", Numeric: true}, {Name: "n", DBType: "INT", Numeric: true}, {Name: "s", DBType: "TEXT"}}
	rows := []exec.Row{{"{1,2,3}", "[1,10)", "$1,234.56", "42", "plain"}, {`{"a,b"}`, nil, "$0.00", "-1", "x\ny"}}
	for _, delim := range []string{",", ";"} {
		rt, _ := newExportWithRuntime(t, exec.NewResult("r", cols, rows), nil,
			`ctx:export_with("csv", args[1], { options = { quoting = "nonnumeric", delimiter = "`+delim+`" } })`)
		p := filepath.Join(t.TempDir(), "n.csv")
		if err := run(t, rt, "xw", p); err != nil {
			t.Fatal(err)
		}
		text := readFile(t, p)
		r := csv.NewReader(strings.NewReader(text))
		r.Comma = rune(delim[0])
		recs, err := r.ReadAll()
		if err != nil {
			t.Fatalf("delimiter %q: not valid CSV: %v\n%s", delim, err, text)
		}
		want := [][]string{{"arr", "rng", "m", "n", "s"}, {"{1,2,3}", "[1,10)", "$1,234.56", "42", "plain"}, {`{"a,b"}`, "", "$0.00", "-1", "x\ny"}}
		if len(recs) != len(want) {
			t.Fatalf("delimiter %q: %d records:\n%s", delim, len(recs), text)
		}
		for i := range want {
			if strings.Join(recs[i], "|") != strings.Join(want[i], "|") {
				t.Errorf("delimiter %q record %d: %q, want %q", delim, i, recs[i], want[i])
			}
		}
		// Plain numbers stay unquoted.
		if !strings.Contains(text, delim+"42"+delim) {
			t.Errorf("delimiter %q: number quoted:\n%s", delim, text)
		}
	}
}

// noTempFiles fails when an export left a temporary file in dir.
func noTempFiles(t *testing.T, dir string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ".*.dbcc-*.tmp"))
	if len(m) > 0 {
		t.Errorf("temporary files left behind: %v", m)
	}
}

// TestExportReplaceIsSafe: with force an existing file is replaced only
// after a complete export; errors and cancel keep it, links stay links.
func TestExportReplaceIsSafe(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id"}}
	rows := []exec.Row{{"1"}, {"2"}}
	dir := t.TempDir()
	mk := func(body string) *Runtime {
		rt, _ := newExportWithRuntime(t, exec.NewResult("r", cols, rows), nil, body)
		mustLoad(t, rt, `dbcc.exporter{ name = "fail", write = function(ctx, src, out)
  out:write("partial\n")
  src:each(function(row) if row[1] == "2" then error("boom") end end)
end }
dbcc.exporter{ name = "swallow", write = function(ctx, src, out)
  out:write("head\n")
  pcall(src.each, src, function(row) out:write(row[1], "\n") end)
end }`)
		return rt
	}
	old := filepath.Join(dir, "old.csv")
	reset := func() {
		os.Remove(old)
		if err := os.WriteFile(old, []byte("OLD"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// An error in write keeps the old file.
	reset()
	if err := run(t, mk(`ctx:export_with("fail", args[1], { force = true })`), "xw", old); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("fail: %v", err)
	}
	if readFile(t, old) != "OLD" {
		t.Errorf("old file changed by a failed export: %q", readFile(t, old))
	}
	noTempFiles(t, dir)

	// A cancel keeps it too, also when the exporter swallows the error.
	for _, name := range []string{"csv", "swallow"} {
		reset()
		rt := mk(`ctx:export_with("` + name + `", args[1], { force = true })`)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c, _ := rt.Command("xw")
		if err := rt.Run(ctx, c, []string{old}); !errors.Is(err, context.Canceled) {
			t.Errorf("%s cancelled: %v", name, err)
		}
		if readFile(t, old) != "OLD" {
			t.Errorf("%s: old file changed by a cancelled export: %q", name, readFile(t, old))
		}
		noTempFiles(t, dir)
	}

	// Success replaces it, with the old permission bits.
	reset()
	if err := run(t, mk(`ctx:export_with("csv", args[1], { force = true })`), "xw", old); err != nil {
		t.Fatal(err)
	}
	if readFile(t, old) != "id\n1\n2\n" {
		t.Errorf("replaced: %q", readFile(t, old))
	}
	if st, _ := os.Stat(old); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
	noTempFiles(t, dir)

	// A symbolic link stays a link; the new content is in its target.
	sub := t.TempDir()
	real := filepath.Join(sub, "real.csv")
	os.WriteFile(real, []byte("OLD"), 0o640)
	link := filepath.Join(dir, "link.csv")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := run(t, mk(`ctx:export_with("fail", args[1], { force = true })`), "xw", link); err == nil {
		t.Error("failing export through a link succeeded")
	}
	if readFile(t, real) != "OLD" {
		t.Errorf("link target changed by a failed export: %q", readFile(t, real))
	}
	if err := run(t, mk(`ctx:export_with("csv", args[1], { force = true })`), "xw", link); err != nil {
		t.Fatal(err)
	}
	if li, err := os.Lstat(link); err != nil || li.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v %v", li, err)
	}
	if readFile(t, real) != "id\n1\n2\n" {
		t.Errorf("link target: %q", readFile(t, real))
	}
	if st, _ := os.Stat(real); st.Mode().Perm() != 0o640 {
		t.Errorf("target mode %v, want 0640", st.Mode().Perm())
	}
	noTempFiles(t, dir)
	noTempFiles(t, sub)

	// A dangling link is refused with force.
	dangling := filepath.Join(dir, "dangling.csv")
	os.Symlink(filepath.Join(sub, "missing"), dangling)
	if err := run(t, mk(`ctx:export_with("csv", args[1], { force = true })`), "xw", dangling); err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Errorf("dangling link: %v", err)
	}
}

// TestExportSwallowedStreamError: an exporter that catches the error of
// src:each with pcall does not turn a truncated export into a success.
func TestExportSwallowedStreamError(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id"}}
	rt, h := newExportWithRuntime(t, exec.NewResult("r", cols, []exec.Row{{"1"}}), nil,
		`ctx:message("n=" .. ctx:export_with("swallow", args[1]))`)
	mustLoad(t, rt, `dbcc.exporter{ name = "swallow", write = function(ctx, src, out)
  out:write("head\n")
  local ok, err = pcall(src.each, src, function(row) error("row failed") end)
  ctx:message("caught: " .. tostring(ok))
end }`)
	p := filepath.Join(t.TempDir(), "s.txt")
	if err := run(t, rt, "xw", p); err == nil || !strings.Contains(err.Error(), "row failed") {
		t.Errorf("swallowed error: %v", err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("truncated export kept")
	}
	if !strings.Contains(h.infos(), "caught: false") || strings.Contains(h.infos(), "n=") {
		t.Errorf("messages: %q", h.infos())
	}
	// A stream error (cancel) that the exporter swallows fails the export as well.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := rt.Command("xw")
	if err := rt.Run(ctx, c, []string{p}); !errors.Is(err, context.Canceled) {
		t.Errorf("swallowed cancel: %v", err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("truncated export kept after cancel")
	}
}

// TestOldExportInsideExporter: ctx:export would stream the same result
// under the running exporter.
func TestOldExportInsideExporter(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id"}}
	rt, _ := newExportWithRuntime(t, exec.NewResult("r", cols, []exec.Row{{"1"}}), nil, `ctx:export_with("inner", args[1])`)
	mustLoad(t, rt, `dbcc.exporter{ name = "inner", write = function(ctx, src, out) ctx:export(out.path .. ".csv") end }`)
	p := filepath.Join(t.TempDir(), "o.txt")
	if err := run(t, rt, "xw", p); err == nil || !strings.Contains(err.Error(), "ctx:export cannot run inside an exporter") {
		t.Errorf("nested ctx:export: %v", err)
	}
	if _, err := os.Stat(p + ".csv"); err == nil {
		t.Error("nested export wrote a file")
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("outer file kept")
	}
}

// TestOldExportReplaceIsSafe: ctx:export with force shares the safe
// replacement of ctx:export_with.
func TestOldExportReplaceIsSafe(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "id"}}
	rt, _ := newExportRuntime(t, exec.NewResult("r", cols, []exec.Row{{"1"}}), nil)
	dir := t.TempDir()
	p := filepath.Join(dir, "o.csv")
	os.WriteFile(p, []byte("OLD"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := rt.Command("x")
	if err := rt.Run(ctx, c, []string{p, "force"}); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	if readFile(t, p) != "OLD" {
		t.Errorf("old file changed: %q", readFile(t, p))
	}
	noTempFiles(t, dir)
	if err := run(t, rt, "x", p, "force"); err != nil || readFile(t, p) != "id\n1\n" {
		t.Errorf("force: %v %q", err, readFile(t, p))
	}
	noTempFiles(t, dir)
}

// TestExportWithChosenPaths: ctx:export_with writes only to a path the
// owner chose in this invocation: a "file" argument or flag, or a path
// from ctx:save_file.
func TestExportWithChosenPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	wd, _ := os.Getwd()
	relOf := func(p string) string {
		r, err := filepath.Rel(wd, p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	h := &formHost{ok: true}
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	eh := &chosenHost{formHost: h, res: exec.NewResult("r", []exec.ColumnInfo{{Name: "id"}}, []exec.Row{{"1"}})}
	rt := New(Env{Workspace: session.NewWorkspace(cfg, nil), Host: eh})
	defer rt.Close()
	b, _ := commands.FS.ReadFile("exporter_csv.lua")
	if _, err := rt.Load("embedded:exporter_csv.lua", string(b), true); err != nil {
		t.Fatal(err)
	}
	constant := filepath.Join(dir, "constant.csv")
	mustLoad(t, rt, `
dbcc.command{ name = "sp", args = { "file", "string" }, run = function(ctx, args)
  ctx:export_with("csv", args[2], { force = true }) end }
dbcc.command{ name = "fl2", flags = { o = "file", s = "string" }, run = function(ctx, args, flags)
  ctx:export_with("csv", flags.s or flags.o, { force = true }) end }
dbcc.command{ name = "sv", run = function(ctx)
  local p = ctx:save_file{ name = "x.csv" }
  ctx:export_with("csv", p, { force = true }) end }
dbcc.command{ name = "st", args = { "string" }, run = function(ctx, args)
  ctx:export_with("csv", args[1], { force = true }) end }
dbcc.command{ name = "c", args = { "file" }, run = function(ctx, args)
  ctx:export_with("csv", "`+constant+`", { force = true }) end }`)
	abs := filepath.Join(dir, "out.csv")
	rel := relOf(abs)
	inHome := filepath.Join(home, "h.csv")
	allowed := []struct{ given, used string }{
		{abs, rel},
		{rel, abs},
		{"./" + rel, rel},
		{"~/h.csv", inHome},
		{inHome, "~/h.csv"},
		{filepath.Join(dir, "sub", "..", "out.csv"), abs},
	}
	for _, c := range allowed {
		os.Remove(abs)
		if err := run(t, rt, "sp", c.given, c.used); err != nil {
			t.Errorf("file argument %q, export to %q: %v", c.given, c.used, err)
		}
	}
	if readFile(t, abs) != "id\n1\n" || readFile(t, inHome) != "id\n1\n" {
		t.Error("exports not written")
	}
	// A file-typed flag grants it; a string flag does not.
	if err := run(t, rt, "fl2", "-o", rel); err != nil {
		t.Errorf("file flag: %v", err)
	}
	notChosen := func(err error, p string) bool {
		return err != nil && strings.Contains(err.Error(), "ctx:export_with: "+p+" was not chosen by the user: pass a path from a \"file\" argument of the command or from ctx:save_file")
	}
	if err := run(t, rt, "fl2", "-o", rel, "-s", constant); !notChosen(err, constant) {
		t.Errorf("string flag: %v", err)
	}
	// ctx:save_file grants the path it returns, for this invocation only.
	saved := filepath.Join(dir, "saved.csv")
	h.path = saved
	if err := run(t, rt, "sv"); err != nil || readFile(t, saved) != "id\n1\n" {
		t.Errorf("save_file: %v", err)
	}
	if err := run(t, rt, "st", saved); !notChosen(err, saved) {
		t.Errorf("path of a previous invocation: %v", err)
	}
	// A string argument and a constant are refused.
	if err := run(t, rt, "st", abs); !notChosen(err, abs) {
		t.Errorf("string argument: %v", err)
	}
	if err := run(t, rt, "c", abs); !notChosen(err, constant) {
		t.Errorf("constant: %v", err)
	}
	// The refusal does not tell whether the file exists.
	missing := filepath.Join(dir, "missing.csv")
	e1, e2 := run(t, rt, "st", abs), run(t, rt, "st", missing)
	if e1 == nil || e2 == nil || strings.ReplaceAll(e1.Error(), abs, "P") != strings.ReplaceAll(e2.Error(), missing, "P") {
		t.Errorf("errors differ: %v / %v", e1, e2)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("refused export created a file")
	}
	if _, err := os.Stat(constant); err == nil {
		t.Error("constant path written")
	}
}

// chosenHost serves a result and answers ctx:save_file.
type chosenHost struct {
	*formHost
	res *exec.Result
}

func (h *chosenHost) ActiveResult() (*exec.Result, exec.View) { return h.res, nil }

// TestOldExportChosenPaths: ctx:export follows the same rule as
// ctx:export_with.
func TestOldExportChosenPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	wd, _ := os.Getwd()
	abs := filepath.Join(dir, "out.csv")
	rel, err := filepath.Rel(wd, abs)
	if err != nil {
		t.Fatal(err)
	}
	constant := filepath.Join(dir, "constant.csv")
	rt, _ := newExportRuntime(t, exec.NewResult("r", []exec.ColumnInfo{{Name: "id"}}, []exec.Row{{"1"}}), nil)
	mustLoad(t, rt, `
dbcc.command{ name = "sp", args = { "file", "string" }, run = function(ctx, args) ctx:export(args[2], { force = true }) end }
dbcc.command{ name = "st", args = { "string" }, run = function(ctx, args) ctx:export(args[1], { force = true }) end }
dbcc.command{ name = "c", args = { "file" }, run = function(ctx, args) ctx:export("`+constant+`", { force = true }) end }`)
	for _, c := range []struct{ given, used string }{
		{rel, abs}, {abs, rel}, {"./" + rel, abs}, {"~/h.csv", filepath.Join(home, "h.csv")}, {filepath.Join(home, "h.csv"), "~/h.csv"},
	} {
		if err := run(t, rt, "sp", c.given, c.used); err != nil {
			t.Errorf("file argument %q, export to %q: %v", c.given, c.used, err)
		}
	}
	if readFile(t, abs) != "id\n1\n" || readFile(t, filepath.Join(home, "h.csv")) != "id\n1\n" {
		t.Error("exports not written")
	}
	notChosen := func(err error, p string) bool {
		return err != nil && strings.Contains(err.Error(), "ctx:export: "+p+" was not chosen by the user: pass a path from a \"file\" argument of the command or from ctx:save_file")
	}
	if err := run(t, rt, "c", abs); !notChosen(err, constant) {
		t.Errorf("constant: %v", err)
	}
	// An existing file and a missing one give the same error; neither is touched.
	os.WriteFile(abs, []byte("OLD"), 0o600)
	before, _ := os.Stat(abs)
	missing := filepath.Join(dir, "missing.csv")
	e1, e2 := run(t, rt, "st", abs), run(t, rt, "st", missing)
	if !notChosen(e1, abs) || !notChosen(e2, missing) ||
		strings.ReplaceAll(e1.Error(), abs, "P") != strings.ReplaceAll(e2.Error(), missing, "P") {
		t.Errorf("string argument: %v / %v", e1, e2)
	}
	// Also for a name ctx:export would reject by its extension.
	if err := run(t, rt, "st", filepath.Join(dir, "x.txt")); !notChosen(err, filepath.Join(dir, "x.txt")) {
		t.Errorf("format checked before the path: %v", err)
	}
	after, _ := os.Stat(abs)
	if readFile(t, abs) != "OLD" || !after.ModTime().Equal(before.ModTime()) {
		t.Error("refused export touched the file")
	}
	for _, p := range []string{missing, constant} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("refused export created %s", p)
		}
	}
}
