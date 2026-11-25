package script

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/script/commands"
	"dbcc/internal/session"
)

// fileHost keeps the results shown and serves the last one as active.
type fileHost struct {
	testHost
	mu       sync.Mutex
	openPath string
}

func (h *fileHost) Interactive() bool { return true }
func (h *fileHost) ActiveResult() (*exec.Result, exec.View) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.results) == 0 {
		return nil, nil
	}
	return h.results[len(h.results)-1], nil
}
func (h *fileHost) Result(r *exec.Result) {
	h.mu.Lock()
	h.results = append(h.results, r)
	h.mu.Unlock()
}
func (h *fileHost) OpenFile(SaveFileOptions) (string, bool, error) {
	return h.openPath, h.openPath != "", nil
}

func (h *fileHost) last(t *testing.T) *exec.Result {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.results) == 0 {
		t.Fatal("no result shown")
	}
	return h.results[len(h.results)-1]
}

// loadDefaults loads every embedded script like command.Registry does.
func loadDefaults(t *testing.T, rt *Runtime) {
	t.Helper()
	names, _ := fs.Glob(commands.FS, "*.lua")
	sort.Strings(names)
	for _, n := range names {
		b, _ := commands.FS.ReadFile(n)
		if _, err := rt.Load("embedded:"+n, string(b), true); err != nil {
			t.Fatal(err)
		}
	}
}

func newFileRuntime(t *testing.T, files *Files) (*Runtime, *fileHost) {
	t.Helper()
	h := &fileHost{}
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	rt := New(Env{Workspace: session.NewWorkspace(cfg, nil), Host: h, Files: files})
	if files != nil {
		files.Use(rt)
	}
	t.Cleanup(rt.Close)
	loadDefaults(t, rt)
	// fx: run the Lua body with args[1] (a file argument) and args[2].
	mustLoad(t, rt, `dbcc.command{ name = "fx", args = { "file", "string?" }, run = function(ctx, args)
  local f = loadstring("local ctx, args = ...; " .. args[2])
  f(ctx, args)
end }`)
	return rt, h
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func rowsText(r *exec.Result) string {
	var lines []string
	for _, row := range r.Rows() {
		var cells []string
		for _, v := range row {
			if v == nil {
				cells = append(cells, "NULL")
			} else {
				cells = append(cells, v.(string))
			}
		}
		lines = append(lines, strings.Join(cells, "|"))
	}
	return strings.Join(lines, "\n")
}

func colsText(r *exec.Result) string {
	var out []string
	for _, c := range r.Columns {
		n := c.Name
		if c.Numeric {
			n += "#"
		}
		out = append(out, n)
	}
	return strings.Join(out, ",")
}

func TestDecode(t *testing.T) {
	cases := []struct {
		in   []byte
		enc  string
		want string
	}{
		{[]byte("Gr\xfc\xdfe \x80"), "windows-1252", "Grüße €"},
		{[]byte("Gr\xfc\xdfe \x80"), "iso-8859-1", "Grüße \u0080"},
		{[]byte("\x8a\x9f\x81"), "windows-1252", "ŠŸ\u0081"},
		{[]byte("ok \xff\xfe x"), "utf-8", "ok \uFFFD x"},
		{[]byte("Grüße"), "utf-8", "Grüße"},
	}
	for _, c := range cases {
		if got := decode(c.in, c.enc); got != c.want {
			t.Errorf("decode(%q, %s) = %q, want %q", c.in, c.enc, got, c.want)
		}
	}
}

func TestCSVParser(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, content, delim, quote string
		want                        string // records joined by "\n", fields by "|", quoted fields marked with *
		err                         string
	}{
		{"basic", "a,b,c\n1,2,3\n", ",", `"`, "a|b|c\n1|2|3", ""},
		{"quotes", "\"a,b\",\"say \"\"hi\"\"\",x\n", ",", `"`, "*a,b|*say \"hi\"|x", ""},
		{"multiline", "1,\"line one\nline two\",3\n4,5,6", ",", `"`, "1|*line one\nline two|3\n4|5|6", ""},
		{"crlf", "a;b\r\n1;\"x\r\ny\"\r\n", ";", `"`, "a|b\n1|*x\ny", ""},
		{"single quote", "'a|b'|'it''s'\n", "|", "'", "*a|b|*it's", ""},
		{"tab", "a\tb\n\"x\ty\"\tz\n", "\t", `"`, "a|b\n*x\ty|z", ""},
		{"empty lines", "a,b\n\n\n1,2\n\n", ",", `"`, "a|b\n1|2", ""},
		{"no final newline", "a,b\n1,2", ",", `"`, "a|b\n1|2", ""},
		{"trailing delimiter", "a,\n,b\n", ",", `"`, "a|\n|b", ""},
		{"empty quoted", "\"\",x,\n", ",", `"`, "*|x|", ""},
		{"text after quote", "\"ab\"cd,e\n", ",", `"`, "*abcd|e", ""},
		{"unicode delimiter", "aöb\n", "ö", `"`, "a|b", ""},
		{"lone CR at the end", "a,b\r\n1,2\r\n3,4\r", ",", `"`, "a|b\n1|2\n3|4", ""},
		// CR alone as the line end throughout (old Mac) is not supported:
		// the file is one line; only the CR at its end is removed.
		{"old Mac", "a,b\r1,2\r", ",", `"`, "a|b\r1|2", ""},
		{"unterminated", "a,b\n1,\"open\n2,3\n", ",", `"`, "", "the quote opened in line 2 is not closed"},
	}
	for _, c := range cases {
		p := writeFile(t, dir, c.name+".csv", c.content)
		rd, err := openFileReader(p)
		if err != nil {
			t.Fatal(err)
		}
		cp := &csvParser{r: rd, delim: c.delim, quote: c.quote}
		var recs []string
		var gotErr error
		for {
			fields, quoted, ok, err := cp.next()
			if err != nil {
				gotErr = err
				break
			}
			if !ok {
				break
			}
			for i := range fields {
				if quoted[i] {
					fields[i] = "*" + fields[i]
				}
			}
			recs = append(recs, strings.Join(fields, "|"))
		}
		rd.close()
		if c.err != "" {
			if gotErr == nil || !strings.Contains(gotErr.Error(), c.err) {
				t.Errorf("%s: want error %q, got %v", c.name, c.err, gotErr)
			}
			continue
		}
		if gotErr != nil || strings.Join(recs, "\n") != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, strings.Join(recs, "\n"), gotErr, c.want)
		}
	}
}

// TestSrcMethods drives src:read/line/peek/encoding/skip_bom from Lua.
func TestSrcMethods(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "s.txt", "\xef\xbb\xbfeins\r\nzwei\ndrei äö")
	rt, h := newFileRuntime(t, nil)
	mustLoad(t, rt, `dbcc.importer{ name = "probe", extensions = { "txt" }, open = function(ctx, src, opts)
  local out = {}
  out[#out + 1] = tostring(src:skip_bom())
  out[#out + 1] = tostring(src:skip_bom())
  out[#out + 1] = src:peek(4)
  out[#out + 1] = src:line()
  local ok, err = pcall(src.encoding, src, "windows-1252")
  out[#out + 1] = tostring(ok)
  out[#out + 1] = src:read(6)
  out[#out + 1] = src:read(100)
  out[#out + 1] = tostring(src:read(5))
  out[#out + 1] = src.name .. ":" .. src.size
  ctx:message(table.concat(out, "|"))
  return { columns = { "x" }, next = function() return nil end }
end }`)
	if err := run(t, rt, "fx", p, `ctx:file_count(args[1], { importer = "probe" })`); err != nil {
		t.Fatal(err)
	}
	want := "true|false|eins|eins|false|zwei\nd|rei äö|nil|s.txt:" + "23"
	if got := h.infos(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// read(n) never cuts a UTF-8 character; encodings decode.
	p2 := writeFile(t, dir, "w.txt", "\xe4\xfc\x80\n")
	h.messages = nil
	mustLoad(t, rt, `dbcc.importer{ name = "enc", open = function(ctx, src, opts)
  src:encoding(opts.e)
  ctx:message(src:read(2) .. "/" .. (src:line() or ""))
  return { columns = { "x" }, next = function() return nil end }
end, options = { { name = "e", type = "string", default = "utf-8" } } }`)
	if err := run(t, rt, "fx", p2, `ctx:file_count(args[1], { importer = "enc", options = { e = "windows-1252" } })`); err != nil {
		t.Fatal(err)
	}
	if got := h.infos(); got != "äü/€" {
		t.Errorf("windows-1252: %q", got)
	}
	p3 := writeFile(t, dir, "u.txt", "äü\n")
	h.messages = nil
	if err := run(t, rt, "fx", p3, `ctx:file_count(args[1], { importer = "enc" })`); err != nil {
		t.Fatal(err)
	}
	if got := h.infos(); got != "ä/ü" {
		t.Errorf("utf-8 read(2): %q", got)
	}
}

func TestImporterDeclaration(t *testing.T) {
	o := `open = function() end`
	cases := map[string]string{
		`dbcc.importer{ name = "x" }`:                                          `"open" must be a function`,
		`dbcc.importer{ ` + o + ` }`:                                           `dbcc.importer: field "name" is required`,
		`dbcc.importer{ name = "X", ` + o + ` }`:                               `invalid importer name`,
		`dbcc.importer{ name = "x", write = 1, ` + o + ` }`:                    `unknown field(s) [write] (name, title, help, extensions, options, open)`,
		`dbcc.importer{ name = "x", options = { { name = "a" } }, ` + o + ` }`: `a: field "type" is required`,
	}
	for src, want := range cases {
		rt, _ := newTestRuntime()
		if _, err := rt.Load("t.lua", src, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", src, want, err)
		}
		if _, ok := rt.Importer("x"); ok {
			t.Errorf("%s: registered", src)
		}
		rt.Close()
	}
	// Override, fallback and listing.
	rt, h := newTestRuntime()
	defer rt.Close()
	if _, err := rt.Load("embedded:importer_x.lua", `dbcc.importer{ name = "x", title = "X", extensions = { "x" }, open = function() end }`, true); err != nil {
		t.Fatal(err)
	}
	rt.Load("/u/bad.lua", `dbcc.importer{ name = "x", title = "bad", open = function() end } error("no")`, false)
	if e, _ := rt.Importer("x"); e.Title != "X" {
		t.Errorf("broken override replaced: %+v", e)
	}
	names, err := rt.Load("/u/x.lua", `dbcc.importer{ name = "x", title = "Mine", open = function() end }
dbcc.command{ name = "l", run = function(ctx) local e = dbcc.importers()[1]; print(e.name, e.title, e.source, e.default) end }`, false)
	if err != nil || strings.Join(names, ",") != "l,importer:x" {
		t.Fatalf("%v %v", names, err)
	}
	if e, _ := rt.Importer("x"); !e.Replaces || e.Default {
		t.Errorf("override: %+v", e)
	}
	run(t, rt, "l")
	if h.infos() != "x\tMine\t/u/x.lua\tfalse" {
		t.Errorf("importers: %q", h.infos())
	}
}

// TestImporterCSV runs importer_csv.lua over many layouts.
func TestImporterCSV(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, content, opts string
		cols, rows          string
		err                 string
	}{
		{"auto comma", "id,name\n1,\"a;b\"\n2,c\n", ``, "id#,name", "1|a;b\n2|c", ""},
		{"auto semicolon with quoted commas", "id;name;note\n1;\"x, y, z\";a\n2;b;\"p,q\"\n", ``, "id#,name,note", "1|x, y, z|a\n2|b|p,q", ""},
		{"auto tab", "a\tb\n1\t2\n", ``, "a#,b#", "1|2", ""},
		{"auto pipe", "a|b|c\n1|2|3\n", ``, "a#,b#,c#", "1|2|3", ""},
		{"explicit", "a;b,c\n1;2,3\n", `delimiter = ";"`, "a#,b,c", "1|2,3", ""},
		{"no header", "1,x\n2,y,z\n", `header = false`, "col1#,col2,col3", "1|x|NULL\n2|y|z", ""},
		{"empty header names", "id,,x\n1,2,3\n", ``, "id#,col2#,x#", "1|2|3", ""},
		{"null text", "a,b\nNULL,\"NULL\"\n,x\n", `null = "NULL"`, "a,b", "NULL|NULL\n|x", ""},
		{"empty is null", "a,b\n,\"\"\n", `empty_is_null = true`, "a,b", "NULL|", ""},
		{"numbers", "n,f,t,e\n1,1.5,x,\n-2,.5,3,\n+3,1e5,4,\n", ``, "n#,f#,t,e", "1|1.5|x|\n-2|.5|3|\n+3|1e5|4|", ""},
		{"numbers off", "n\n1\n", `numbers = false`, "n", "1", ""},
		{"skip", "# title\n# more\na,b\n1,2\n", `skip = 2`, "a#,b#", "1|2", ""},
		{"bom", "\xef\xbb\xbfa,b\n1,2\n", ``, "a#,b#", "1|2", ""},
		{"short rows", "a,b,c\n1\n2,3\n", ``, "a#,b#,c", "1|NULL|NULL\n2|3|NULL", ""},
		{"long rows", "a,b\n1,2,3\n", ``, "", "", "record 1 has 3 fields, more than the 2 columns"},
		{"windows-1252", "Name;Preis\nM\xfcller;5 \x80\n", `encoding = "windows-1252"`, "Name,Preis", "Müller|5 €", ""},
		{"iso-8859-1", "Name\nM\xfcller\n", `encoding = "iso-8859-1"`, "Name", "Müller", ""},
		{"invalid utf-8", "x\nok \xff\n", ``, "x", "ok \uFFFD", ""},
		{"single quote", "a,b\n'x,y',z\n", `quote = "'"`, "a,b", "x,y|z", ""},
		{"multi-line", "a,b\n\"1\n2\",x\n", ``, "a,b", "1\n2|x", ""},
		{"unterminated", "a\n\"x\n", ``, "", "", "the quote opened in line 2 is not closed"},
	}
	for i, c := range cases {
		files := NewFiles(0, 0, false)
		rt, h := newFileRuntime(t, files)
		p := writeFile(t, dir, "c"+string(rune('a'+i))+".csv", c.content)
		err := run(t, rt, "fx", p, `ctx:file_grid(args[1], { options = { `+c.opts+` } })`)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: want error %q, got %v", c.name, c.err, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		r := h.last(t)
		r.FetchAll(100)
		if colsText(r) != c.cols || rowsText(r) != c.rows {
			t.Errorf("%s:\n cols %q want %q\n rows %q want %q", c.name, colsText(r), c.cols, rowsText(r), c.rows)
		}
	}
}

func bigCSV(t *testing.T, dir, name string, n int) string {
	var b strings.Builder
	b.WriteString("n,v\n")
	for i := 1; i <= n; i++ {
		b.WriteString(itoa(i) + ",v" + itoa(i) + "\n")
	}
	return writeFile(t, dir, name, b.String())
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Replace(formatNumber(float64(i)), " ", "", -1))
}

func firstN(r *exec.Result) string {
	rows := r.Rows()
	if len(rows) == 0 {
		return "-"
	}
	return rows[0][0].(string) + ".." + rows[len(rows)-1][0].(string)
}

// TestFileResultPaging: lazy chunks and pages over a file larger than
// several pages, forward on the open cursor and back by reopening.
func TestFileResultPaging(t *testing.T) {
	dir := t.TempDir()
	p := bigCSV(t, dir, "big.csv", 35)
	files := NewFiles(10, 4, true)
	rt, _ := newFileRuntime(t, files)
	res, err := rt.FileResult(&exec.FileOrigin{Path: p, Importer: "csv", Options: mustOpts(t, rt, "csv")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Len() != 4 || res.Title != "big.csv" || res.File == nil || !res.More() {
		t.Fatalf("first chunk: %d rows %q", res.Len(), res.Title)
	}
	res.FetchAll(4)
	if firstN(res) != "1..10" || !res.HasNextPage() {
		t.Fatalf("page 1: %s", firstN(res))
	}
	ctx := context.Background()
	for page, want := range []string{"11..14", "21..24", "31..34"} {
		if err := res.NextPage(ctx); err != nil || firstN(res) != want {
			t.Fatalf("page %d: %s %v", page+2, firstN(res), err)
		}
		res.FetchAll(4)
	}
	if firstN(res) != "31..35" || res.HasNextPage() {
		t.Fatalf("last page %s", firstN(res))
	}
	if err := res.PrevPage(ctx); err != nil || firstN(res) != "21..24" || res.Page() != 3 {
		t.Fatalf("back: %s %v", firstN(res), err)
	}
	if err := res.PrevPage(ctx); err != nil || firstN(res) != "11..14" {
		t.Fatalf("back again: %s %v", firstN(res), err)
	}
	// Export of the whole file from page 2 (Stream reopens).
	n := 0
	if err := res.Stream(ctx, 7, func(rows []exec.Row) error { n += len(rows); return nil }); err != nil || n != 35 {
		t.Errorf("stream %d %v", n, err)
	}
	// The file changes: paging reads what is there; other columns fail.
	writeFile(t, dir, "big.csv", "n,v\n100,x\n")
	if err := res.PrevPage(ctx); err != nil || firstN(res) != "100..100" {
		t.Errorf("changed file: %s %v", firstN(res), err)
	}
	res.Release()
	// Other columns after a change: the existing ErrColumnsChanged.
	bigCSV(t, dir, "big.csv", 25)
	res, err = rt.FileResult(&exec.FileOrigin{Path: p, Importer: "csv", Options: mustOpts(t, rt, "csv")}, "")
	if err != nil {
		t.Fatal(err)
	}
	res.FetchAll(4)
	if err := res.NextPage(ctx); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "big.csv", "other\n1\n")
	if err := res.PrevPage(ctx); !errors.Is(err, exec.ErrColumnsChanged) {
		t.Errorf("columns changed: %v", err)
	}
	res.Release()
	rt.srcMu.Lock()
	open := len(rt.sources)
	rt.srcMu.Unlock()
	if open != 0 {
		t.Errorf("%d file sources still open", open)
	}
}

func mustOpts(t *testing.T, rt *Runtime, name string) map[string]any {
	t.Helper()
	imp, ok := rt.Importer(name)
	if !ok {
		t.Fatal("no importer " + name)
	}
	v, err := ResolveOptions(imp.Options, nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFilePrimitives(t *testing.T) {
	dir := t.TempDir()
	p := bigCSV(t, dir, "d.csv", 25)
	files := NewFiles(10, 5, true)
	rt, h := newFileRuntime(t, files)
	// limit, tail, count, info
	if err := run(t, rt, "fx", p, `ctx:file_grid(args[1], { limit = 3, title = "T" })`); err != nil {
		t.Fatal(err)
	}
	r := h.last(t)
	r.FetchAll(10)
	if firstN(r) != "1..3" || r.Title != "T" || r.More() || r.HasNextPage() {
		t.Errorf("limit: %s %q", firstN(r), r.Title)
	}
	if err := run(t, rt, "fx", p, `ctx:file_grid(args[1], { tail = 4 })`); err != nil {
		t.Fatal(err)
	}
	r = h.last(t)
	if firstN(r) != "22..25" || r.Cursor() != nil || r.File == nil || r.File.Tail != 4 {
		t.Errorf("tail: %s", firstN(r))
	}
	if err := run(t, rt, "fx", p, `ctx:message("n=" .. ctx:file_count(args[1]))
local i = ctx:file_info(ctx:resolve("file:" .. args[1]))
ctx:message(i.name .. " " .. i.size .. " " .. #i.modified .. " " .. i.importer .. " " .. i.options.delimiter .. " " .. #i.columns .. " " .. i.columns[1].name .. tostring(i.columns[1].numeric))
local o = ctx:resolve("file:" .. args[1])
ctx:message(o.kind .. " " .. o.name .. " " .. tostring(o) .. " " .. tostring(ctx:on(o) == ctx))`); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	want := "n=25\nd.csv " + itoa(int(st.Size())) + " 19 csv auto 2 ntrue\nfile d.csv " + p + " true"
	if got := h.infos(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// Unknown types and files.
	h.messages = nil
	for body, want := range map[string]string{
		`ctx:file_grid(args[1], { importer = "xml" })`:              `unknown import type "xml" (types: csv)`,
		`ctx:file_grid(args[1], { options = { delimiter = "#" } })`: `import type csv: option delimiter`,
		`ctx:file_grid(args[1], { options = { nope = 1 } })`:        `unknown option "nope"`,
		`ctx:file_grid(args[1], { limit = "x" })`:                   `opts.limit must be a number`,
	} {
		if err := run(t, rt, "fx", p, body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", body, want, err)
		}
	}
	dat := writeFile(t, dir, "x.dat", "a\n")
	if err := run(t, rt, "fx", dat, `ctx:file_count(args[1])`); err == nil || !strings.Contains(err.Error(), `no import type for "x.dat" (extensions: .csv, .tsv, .txt; types: csv)`) {
		t.Errorf("unknown extension: %v", err)
	}
	if err := run(t, rt, "fx", filepath.Join(dir, "missing.csv"), `ctx:file_count(args[1])`); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("missing file: %v", err)
	}
}

// TestFileRememberedSettings: explicit settings are remembered per path
// for the session, so wc after open counts the same table.
func TestFileRememberedSettings(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "r.csv", "a;b,c\n1;2,3\n4;5,6\n")
	cols := func(rt *Runtime, h *fileHost) string {
		h.messages = nil
		if err := run(t, rt, "fx", p, `local i = ctx:file_info(args[1]); local n = {}
for _, c in ipairs(i.columns) do n[#n + 1] = c.name end; ctx:message(table.concat(n, "|"))`); err != nil {
			t.Fatal(err)
		}
		return h.infos()
	}
	files := NewFiles(0, 0, true)
	rt, h := newFileRuntime(t, files)
	if got := cols(rt, h); got != "a;b|c" {
		t.Fatalf("default: %q", got)
	}
	if err := run(t, rt, "fx", p, `ctx:file_grid(args[1], { importer = "csv", options = { delimiter = ";" } })`); err != nil {
		t.Fatal(err)
	}
	if got := cols(rt, h); got != "a|b,c" {
		t.Errorf("remembered: %q", got)
	}
	// It survives a reload (a new runtime on the same Files).
	rt2, h2 := newFileRuntime(t, files)
	if got := cols(rt2, h2); got != "a|b,c" {
		t.Errorf("after reload: %q", got)
	}
	// Headless has no memory.
	rt3, h3 := newFileRuntime(t, NewFiles(0, 0, false))
	run(t, rt3, "fx", p, `ctx:file_grid(args[1], { options = { delimiter = ";" } })`)
	if got := cols(rt3, h3); got != "a;b|c" {
		t.Errorf("without memory: %q", got)
	}
}

// TestFileChosenPaths: reading follows the chosen-path rule.
func TestFileChosenPaths(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.csv", "a\n1\n")
	rt, h := newFileRuntime(t, nil)
	mustLoad(t, rt, `
dbcc.command{ name = "obj", args = { "object" }, run = function(ctx, args) ctx:message(ctx:file_count(ctx:resolve(args[1]))) end }
dbcc.command{ name = "objflag", flags = { s = "object" }, run = function(ctx, args, flags) ctx:message(ctx:file_count(flags.s)) end }
dbcc.command{ name = "str", args = { "string" }, run = function(ctx, args) ctx:message(ctx:file_count(args[1])) end }
dbcc.command{ name = "dlg", run = function(ctx) ctx:message(ctx:file_count(ctx:open_file())) end }`)
	for _, c := range [][]string{{"obj", "file:" + p}, {"objflag", "-s", "file:" + p}, {"fx", p, `ctx:message(ctx:file_count(args[1]))`}} {
		h.messages = nil
		if err := run(t, rt, c[0], c[1:]...); err != nil || h.infos() != "1" {
			t.Errorf("%v: %v %q", c, err, h.infos())
		}
	}
	h.openPath = p
	h.messages = nil
	if err := run(t, rt, "dlg"); err != nil || h.infos() != "1" {
		t.Errorf("open_file: %v", err)
	}
	missing := filepath.Join(dir, "missing.csv")
	refused := func(err error, path string) bool {
		return err != nil && strings.Contains(err.Error(), "ctx:file_count: "+path+" was not chosen by the user")
	}
	e1, e2 := run(t, rt, "str", p), run(t, rt, "str", missing)
	if !refused(e1, p) || !refused(e2, missing) || strings.ReplaceAll(e1.Error(), p, "P") != strings.ReplaceAll(e2.Error(), missing, "P") {
		t.Errorf("string argument: %v / %v", e1, e2)
	}
	// A plain object name (not file:) grants nothing.
	if err := run(t, rt, "obj", "c.csv"); err == nil {
		t.Error("object without file: read a file")
	}
}

// TestFileReloadClosesCursors: closing the runtime (reload) closes its
// open file cursors; the result becomes partial and pages through the
// new runtime; without the importer there is a clear error.
func TestFileReloadClosesCursors(t *testing.T) {
	dir := t.TempDir()
	p := bigCSV(t, dir, "z.csv", 30)
	files := NewFiles(10, 5, true)
	rt1, _ := newFileRuntime(t, files)
	res, err := rt1.FileResult(&exec.FileOrigin{Path: p, Importer: "csv", Options: mustOpts(t, rt1, "csv")}, "")
	if err != nil {
		t.Fatal(err)
	}
	rt2, _ := newFileRuntime(t, files)
	files.Use(rt2)
	rt1.Close()
	if res.Cursor() == nil || !res.Cursor().Done() || !strings.Contains(res.Status(), "partial") {
		t.Fatalf("after reload: status %q", res.Status())
	}
	if err := res.NextPage(context.Background()); err != nil || firstN(res) != "11..15" {
		t.Fatalf("paging after reload: %s %v", firstN(res), err)
	}
	h := &fileHost{}
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	rt3 := New(Env{Workspace: session.NewWorkspace(cfg, nil), Host: h, Files: files})
	defer rt3.Close()
	files.Use(rt3) // no importers at all
	if err := res.PrevPage(context.Background()); err == nil || !strings.Contains(err.Error(), `import type "csv" is no longer registered`) {
		t.Errorf("without the importer: %v", err)
	}
}

// TestFileInterruptAndCloseDuringFetch: an importer stuck in an endless
// loop outside a command is stopped by Interrupt (F8) and by closing the
// result (a tab closed on the UI goroutine), without waiting (-race).
func TestFileInterruptAndCloseDuringFetch(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "l.loop", "x\n")
	files := NewFiles(10, 5, true)
	rt, _ := newFileRuntime(t, files)
	mustLoad(t, rt, `dbcc.importer{ name = "loop", extensions = { "loop" }, open = function(ctx, src, opts)
  local k = 0
  return { columns = { "x" }, next = function()
    k = k + 1
    if k == 1 then return { "first" } end
    while true do end
  end }
end }`)
	// The first chunk already loops: use a 1-row chunk for the start.
	files.FetchSize = 1
	res, err := rt.FileResult(&exec.FileOrigin{Path: p, Importer: "loop", Options: map[string]any{}}, "")
	if err != nil || res.Len() != 1 {
		t.Fatalf("open: %v", err)
	}
	for _, stop := range []string{"interrupt", "close"} {
		done := make(chan error, 1)
		go func() { _, err := res.Fetch(5); done <- err }() // the worker
		time.Sleep(50 * time.Millisecond)
		start := time.Now()
		if stop == "interrupt" {
			if !rt.Interrupt() {
				t.Error("Interrupt found nothing to stop")
			}
		} else {
			res.Release()
			if time.Since(start) > time.Second {
				t.Error("Release waited for the read")
			}
		}
		select {
		case err := <-done:
			if stop == "interrupt" && (err == nil || !errors.Is(err, context.Canceled)) {
				t.Errorf("interrupted fetch: %v", err)
			}
			if stop == "close" && err != nil && !errors.Is(err, exec.ErrCursorClosed) {
				t.Errorf("closed fetch: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s did not stop the importer", stop)
		}
		if stop == "interrupt" {
			// The cursor ended with the error; open again for the next round.
			res, err = rt.FileResult(&exec.FileOrigin{Path: p, Importer: "loop", Options: map[string]any{}}, "")
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// The runtime still works afterwards.
	if err := run(t, rt, "fx", p, `ctx:message("ok")`); err != nil {
		t.Errorf("runtime after interrupts: %v", err)
	}
}

// TestFileExportRoundTrip: export results as CSV with several option sets
// and open them again with matching options; also export a file result
// that must be reopened (nested Lua: export -> stream -> importer).
func TestFileExportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cols := []exec.ColumnInfo{{Name: "id", Numeric: true}, {Name: "text"}, {Name: "note"}}
	rows := []exec.Row{{"1", "a,b", nil}, {"2", `say "hi"`, "x\ny"}, {"3", "", "äöü €"}, {"4", " lead", "trail "}}
	sets := []struct{ export, open string }{
		{``, ``},
		{`delimiter = ";", quoting = "all"`, `delimiter = ";"`},
		{`delimiter = "\t", null = "NULL"`, `delimiter = "\t", null = "NULL"`},
		{`quote = "'", quoting = "nonnumeric", line_ending = "crlf", bom = true`, `quote = "'"`},
	}
	for i, set := range sets {
		h := &fileHost{}
		cfg := &config.Config{Profiles: map[string]*config.Profile{}}
		files := NewFiles(0, 0, false)
		rt := New(Env{Workspace: session.NewWorkspace(cfg, nil), Host: h, Files: files})
		files.Use(rt)
		loadDefaults(t, rt)
		mustLoad(t, rt, `dbcc.command{ name = "rt", args = { "file" }, run = function(ctx, args)
  ctx:export_with("csv", args[1], { options = { `+set.export+` } })
  ctx:file_grid(args[1], { options = { `+set.open+` } })
end }`)
		h.results = []*exec.Result{exec.NewResult("src", cols, rows)}
		p := filepath.Join(dir, "rt"+itoa(i)+".csv")
		if err := run(t, rt, "rt", p); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
		r := h.last(t)
		r.FetchAll(100)
		want := "1|a,b|NULL\n2|say \"hi\"|x\ny\n3||äöü €\n4| lead|trail "
		if i != 2 {
			// Without a NULL text NULL is written as an empty field and
			// read back as an empty string.
			want = strings.Replace(want, "1|a,b|NULL", "1|a,b|", 1)
		}
		if got := rowsText(r); got != want || colsText(r) != "id#,text,note" {
			t.Errorf("set %d: cols %q rows\n%q\nwant\n%q", i, colsText(r), got, want)
		}
		rt.Close()
	}
	// Export a file result from page 2: the stream reopens the file
	// through the importer inside the running export (re-entrant Lua).
	p := bigCSV(t, dir, "src.csv", 23)
	files := NewFiles(10, 5, false)
	rt, h := newFileRuntime(t, files)
	res, err := rt.FileResult(&exec.FileOrigin{Path: p, Importer: "csv", Options: mustOpts(t, rt, "csv")}, "")
	if err != nil {
		t.Fatal(err)
	}
	res.FetchAll(5)
	if err := res.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.results = []*exec.Result{res}
	out := filepath.Join(dir, "out.json")
	if err := run(t, rt, "fx", out, `ctx:message(ctx:export_with("json", args[1], { options = { pretty = false } }))`); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if h.infos() != "23" || !strings.HasPrefix(string(b), `[{"n":"1","v":"v1"},`) || !strings.Contains(string(b), `{"n":"23","v":"v23"}]`) {
		t.Errorf("export of a file result: %q\n%s", h.infos(), b)
	}
}

// TestUserImporter: a pure-Lua importer (fixed-width columns) installed
// as a user script.
func TestUserImporter(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "f.fw", "id   name      \n1    Anna      \n22   Bob       \n")
	rt, h := newFileRuntime(t, NewFiles(0, 0, false))
	if _, err := rt.Load("/home/u/.config/dbcc/commands/fixed.lua", `
dbcc.importer{
  name = "fixed", title = "Fixed-width columns", extensions = { "fw" },
  options = { { name = "widths", type = "string", default = "5,10" } },
  open = function(ctx, src, opts)
    local widths = {}
    for w in opts.widths:gmatch("%d+") do widths[#widths + 1] = tonumber(w) end
    local function split(line)
      local out, pos = {}, 1
      for i, w in ipairs(widths) do
        out[i] = line:sub(pos, pos + w - 1):match("^%s*(.-)%s*$")
        pos = pos + w
      end
      return out
    end
    local header = split(src:line())
    return { columns = header, next = function()
      local l = src:line()
      if l == nil then return nil end
      local r = split(l)
      r[1] = tonumber(r[1])
      return r
    end }
  end,
}`, false); err != nil {
		t.Fatal(err)
	}
	if err := run(t, rt, "fx", p, `ctx:file_grid(args[1])`); err != nil {
		t.Fatal(err)
	}
	r := h.last(t)
	if colsText(r) != "id,name" || rowsText(r) != "1|Anna\n22|Bob" {
		t.Errorf("fixed width: %q %q", colsText(r), rowsText(r))
	}
}

// TestFileInterruptLeavesPartial: F8 (Interrupt) during a background read
// leaves the result partial with a next page, and paging recovers.
func TestFileInterruptLeavesPartial(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "b.block", "x\n")
	files := NewFiles(10, 5, true)
	rt, _ := newFileRuntime(t, files)
	mustLoad(t, rt, `local opens = 0
dbcc.importer{ name = "block", extensions = { "block" }, open = function(ctx, src, opts)
  opens = opens + 1
  local first, k = opens == 1, 0
  return { columns = { "n" }, next = function()
    k = k + 1
    if k > 30 then return nil end
    if first and k == 6 then while true do end end -- blocks at record 6 the first time
    return { "r" .. k }
  end }
end }`)
	res, err := rt.FileResult(&exec.FileOrigin{Path: p, Importer: "block", Options: map[string]any{}}, "")
	if err != nil || res.Len() != 5 {
		t.Fatalf("open: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := res.Fetch(5); done <- err }()
	time.Sleep(50 * time.Millisecond)
	rt.Interrupt()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("fetch: %v", err)
	}
	if !res.Partial || !res.HasNextPage() || !strings.Contains(res.Status(), "partial") || res.More() {
		t.Errorf("after F8: partial %v next %v status %q", res.Partial, res.HasNextPage(), res.Status())
	}
	if err := res.NextPage(context.Background()); err != nil || firstN(res) != "r11..r15" {
		t.Errorf("next page after F8: %s %v", firstN(res), err)
	}
	if err := res.PrevPage(context.Background()); err != nil || firstN(res) != "r1..r5" {
		t.Errorf("back: %s %v", firstN(res), err)
	}
	res.Release()
	if n := rt.OpenFiles(); n != 0 {
		t.Errorf("%d files open", n)
	}
}

// TestFileReopenFailureClosesBoth: when the reopen of a page fails after
// the page moved to the new cursor, both cursors are closed.
func TestFileReopenFailureClosesBoth(t *testing.T) {
	dir := t.TempDir()
	p := bigCSV(t, dir, "f.csv", 30)
	files := NewFiles(10, 10, true)
	rt, _ := newFileRuntime(t, files)
	res, err := rt.FileResult(&exec.FileOrigin{Path: p, Importer: "csv", Options: mustOpts(t, rt, "csv")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := res.NextPage(context.Background()); err != nil || rt.OpenFiles() != 1 {
		t.Fatalf("page 2: %v, %d open", err, rt.OpenFiles())
	}
	// Record 11 now has a field too many: page 1 loads, the look-ahead fails.
	var b strings.Builder
	b.WriteString("n,v\n")
	for i := 1; i <= 30; i++ {
		b.WriteString(itoa(i) + ",v")
		if i == 11 {
			b.WriteString(",extra")
		}
		b.WriteString("\n")
	}
	writeFile(t, dir, "f.csv", b.String())
	if err := res.PrevPage(context.Background()); err == nil || !strings.Contains(err.Error(), "record 11 has 3 fields") {
		t.Errorf("prev page: %v", err)
	}
	if n := rt.OpenFiles(); n != 0 {
		t.Errorf("%d file sources still open after the failed reopen", n)
	}
}

// TestFileLimitTailZeroAndHuge: 0 rows keep the columns; a huge tail does
// not allocate for n rows first.
func TestFileLimitTailZeroAndHuge(t *testing.T) {
	dir := t.TempDir()
	p := bigCSV(t, dir, "s.csv", 3)
	rt, h := newFileRuntime(t, NewFiles(0, 0, false))
	for _, body := range []string{`ctx:file_grid(args[1], { limit = 0 })`, `ctx:file_grid(args[1], { tail = 0 })`} {
		if err := run(t, rt, "fx", p, body); err != nil {
			t.Fatal(err)
		}
		r := h.last(t)
		r.FetchAll(10)
		if r.Len() != 0 || colsText(r) != "n#,v" || r.More() {
			t.Errorf("%s: %d rows, cols %q", body, r.Len(), colsText(r))
		}
	}
	if err := run(t, rt, "fx", p, `ctx:file_grid(args[1], { tail = 1000000000 })`); err != nil {
		t.Fatal(err)
	}
	if r := h.last(t); firstN(r) != "1..3" {
		t.Errorf("huge tail: %s", firstN(r))
	}
}

// TestFilePrefixOnFileArguments: file:<path> works for "file" arguments
// (open file:x.csv, export file:out.csv) like for object arguments.
func TestFilePrefixOnFileArguments(t *testing.T) {
	dir := t.TempDir()
	p := bigCSV(t, dir, "in.csv", 2)
	rt, h := newFileRuntime(t, NewFiles(0, 0, false))
	if err := run(t, rt, "open", "file:"+p); err != nil {
		t.Fatalf("open file:: %v", err)
	}
	out := filepath.Join(dir, "out.csv")
	if err := run(t, rt, "export", "file:"+out); err != nil {
		t.Fatalf("export file:: %v", err)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != "n,v\n1,v1\n2,v2\n" {
		t.Errorf("exported %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "file:out.csv")); err == nil {
		t.Error("a file named file:out.csv was written")
	}
	if !strings.Contains(h.infos(), "rows written to "+out+" (csv)") {
		t.Errorf("message %q", h.infos())
	}
}

// TestCSVDecimalComma: with decimal="," columns of decimal-comma numbers
// (grouping dots allowed, strictly) are numeric and declared so; the text
// is unchanged. With "." nothing changes.
func TestCSVDecimalComma(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "de.csv", "betrag;anzahl;tsd;falsch\n1200,50;3;1.234;1.23,4\n-3,5;-2;12.000.000;1\n")
	decimals := func(r *exec.Result) string {
		var out []string
		for _, c := range r.Columns {
			d := c.Decimal
			if d == 0 {
				d = '.'
			}
			out = append(out, string(d))
		}
		return strings.Join(out, "")
	}
	for opts, want := range map[string][2]string{
		`delimiter = ";"`:                    {"betrag,anzahl#,tsd,falsch", "...."},
		`delimiter = ";", decimal = ","`:     {"betrag#,anzahl#,tsd#,falsch", ",,,."},
		`delimiter = ";", decimal = "comma"`: {"betrag#,anzahl#,tsd#,falsch", ",,,."},
	} {
		rt, h := newFileRuntime(t, NewFiles(0, 0, false))
		if err := run(t, rt, "fx", p, `ctx:file_grid(args[1], { options = { `+opts+` } })`); err != nil {
			t.Fatal(err)
		}
		r := h.last(t)
		if colsText(r) != want[0] || decimals(r) != want[1] || rowsText(r) != "1200,50|3|1.234|1.23,4\n-3,5|-2|12.000.000|1" {
			t.Errorf("%s: %q %q %q", opts, colsText(r), decimals(r), rowsText(r))
		}
	}
}

// TestImporterColumnDecimal: the decimal field of columns is validated and
// reaches exporters (src.columns) and ctx:file_info.
func TestImporterColumnDecimal(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "x.dec", "x\n")
	for decl, want := range map[string]string{
		`{ name = "a", numeric = true, decimal = ";" }`: `columns[1].decimal must be "." or ","`,
		`{ name = "a", decimal = "," }`:                 `columns[1].decimal needs numeric = true`,
		`{ name = "a", numeric = true, decimal = 1 }`:   `columns[1].decimal must be "." or ","`,
	} {
		rt, _ := newFileRuntime(t, nil)
		mustLoad(t, rt, `dbcc.importer{ name = "dec", extensions = { "dec" }, open = function()
  return { columns = { `+decl+` }, next = function() return nil end } end }`)
		if err := run(t, rt, "fx", p, `ctx:file_count(args[1])`); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", decl, want, err)
		}
	}
	rt, h := newFileRuntime(t, nil)
	mustLoad(t, rt, `dbcc.importer{ name = "dec", extensions = { "dec" }, open = function()
  local k = 0
  return { columns = { { name = "a", numeric = true, decimal = "," }, "b" }, next = function()
    k = k + 1; if k > 1 then return nil end; return { "1,5", "x" } end } end }
dbcc.exporter{ name = "cols", write = function(ctx, src, out)
  for _, c in ipairs(src.columns) do out:write(c.name, ":", tostring(c.numeric), ":", c.decimal, " ") end
end }`)
	out := filepath.Join(dir, "o.txt")
	if err := run(t, rt, "fx", p, `local i = ctx:file_info(args[1])
ctx:message(i.columns[1].decimal .. i.columns[2].decimal .. " " .. dbcc.fmt.number("1.234,5", ",") .. " " .. tostring(dbcc.fmt.number("1,2,3", ",")) .. " " .. dbcc.fmt.number("2.5"))
ctx:file_grid(args[1])`); err != nil {
		t.Fatal(err)
	}
	if h.infos() != ",. 1234.5 nil 2.5" {
		t.Errorf("file_info/fmt.number: %q", h.infos())
	}
	if err := run(t, rt, "fx", out, `ctx:export_with("cols", args[1])`); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "a:true:, b:false:. " {
		t.Errorf("src.columns: %q", b)
	}
}

// TestDecimalCommaExportRoundTrip: a decimal-comma column exported as CSV
// with quoting "nonnumeric" and delimiter "," is quoted (it contains the
// delimiter) and reads back unchanged.
func TestDecimalCommaExportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "de.csv", "betrag;text\n1.234,50;a\n-3,5;b\n7;c\n")
	out := filepath.Join(dir, "out.csv")
	rt, h := newFileRuntime(t, NewFiles(0, 0, false))
	mustLoad(t, rt, `dbcc.command{ name = "rt", args = { "file", "file" }, run = function(ctx, args)
  ctx:file_grid(args[1], { options = { delimiter = ";", decimal = "," } })
  ctx:export_with("csv", args[2], { options = { quoting = "nonnumeric" } })
  ctx:file_grid(args[2], { options = { delimiter = ",", decimal = "," } })
end }`)
	if err := run(t, rt, "rt", src, out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "\"betrag\",\"text\"\n\"1.234,50\",\"a\"\n\"-3,5\",\"b\"\n7,\"c\"\n" {
		t.Errorf("exported %q", b)
	}
	r := h.last(t)
	if rowsText(r) != "1.234,50|a\n-3,5|b\n7|c" || colsText(r) != "betrag#,text" || r.Columns[0].Decimal != ',' {
		t.Errorf("read back: %q %q", colsText(r), rowsText(r))
	}
}
