package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbcc/internal/exec"
	"dbcc/internal/script"
)

// openHost adds the open dialog to the scripted dialog host.
type openHost struct {
	dialogHost
	openPath string
}

func (h *openHost) OpenFile(o script.SaveFileOptions) (string, bool, error) {
	h.calls = append(h.calls, "openfile")
	h.saveOpts = o
	return h.openPath, h.openPath != "", nil
}

func filesRegistry(t *testing.T, h *openHost) *Registry {
	t.Helper()
	r, _ := newRegistry(t, nil)
	rt := script.New(script.Env{Workspace: r.Runtime().Env().Workspace, Host: h, Files: script.NewFiles(0, 0, false)})
	t.Cleanup(rt.Close)
	reg := New(rt)
	if err := reg.LoadDefaults(); err != nil {
		t.Fatal(err)
	}
	return reg
}

func shown(h *openHost) *exec.Result {
	if len(h.results) == 0 {
		return nil
	}
	return h.results[len(h.results)-1]
}

func rowsOf(r *exec.Result) string {
	var out []string
	for _, row := range r.Rows() {
		var cells []string
		for _, v := range row {
			if v == nil {
				cells = append(cells, "NULL")
			} else {
				cells = append(cells, v.(string))
			}
		}
		out = append(out, strings.Join(cells, "|"))
	}
	return strings.Join(out, "\n")
}

func TestFileCommands(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "kunden.csv")
	os.WriteFile(p, []byte("id;name\n1;Anna\n2;Bob\n3;Cem\n"), 0o600)
	h := &openHost{}
	r := filesRegistry(t, h)
	ctx := context.Background()
	cases := []struct{ line, cols, rows string }{
		{"ll file:" + p, "id,name", "1|Anna\n2|Bob\n3|Cem"},
		{"head file:" + p + " 2", "id,name", "1|Anna\n2|Bob"},
		{"tail file:" + p + " 2", "id,name", "2|Bob\n3|Cem"},
		{"wc file:" + p, "rows,object", "3|" + p},
		{"open " + p, "id,name", "1|Anna\n2|Bob\n3|Cem"},
		{"open file:" + p, "id,name", "1|Anna\n2|Bob\n3|Cem"},
		{"head file:" + p + " 0", "id,name", ""},
		{"tail file:" + p + " 0", "id,name", ""},
		{"tail file:" + p + " 1000000000", "id,name", "1|Anna\n2|Bob\n3|Cem"},
		{"open -t csv -o delimiter=comma -o header=false " + p, "col1", "id;name\n1;Anna\n2;Bob\n3;Cem"},
	}
	for _, c := range cases {
		h.results = nil
		if err := r.Dispatch(ctx, c.line); err != nil {
			t.Errorf("%s: %v", c.line, err)
			continue
		}
		res := shown(h)
		var names []string
		for _, col := range res.Columns {
			names = append(names, col.Name)
		}
		if strings.Join(names, ",") != c.cols || rowsOf(res) != c.rows {
			t.Errorf("%s:\n cols %v\n rows %q, want %q", c.line, names, rowsOf(res), c.rows)
		}
	}
	h.texts = nil
	if err := r.Dispatch(ctx, "cat file:"+p); err != nil || len(h.texts) != 1 {
		t.Fatalf("cat: %v %q", err, h.texts)
	}
	for _, want := range []string{"file:     " + p, "size:     27 B", "modified: ", "import:   csv (", `delimiter="auto"`, "# | column | numeric", "1 | id     | yes", "2 | name   | no"} {
		if !strings.Contains(h.texts[0], want) {
			t.Errorf("cat lacks %q:\n%s", want, h.texts[0])
		}
	}
	// Commands that cannot work on a file fail cleanly, naming the kind.
	for line, want := range map[string]string{
		"tail -f file:" + p:          "tail: -f and -k are not available for files",
		"tail -k id file:" + p:       "tail: -f and -k are not available for files",
		"cp file:" + p + " x":        "cp: kunden.csv is a file, not a table or view",
		"mv file:" + p + " x":        "mv: kunden.csv is a file; mv renames tables and views",
		"edit file:" + p:             "is a file, not a view",
		"cat -t view file:" + p:      "is a file, not a view",
		"ll file:" + dir + "/no.csv": "no such file",
		"wc file:":                   "a file path is missing",
		"open " + dir + "/x.dat":     `no import type for "x.dat"`,
		"open -t xml " + p:           `open: unknown import type "xml" (types: csv)`,
		"open -o delimiter " + p:     `-o needs name=value`,
	} {
		if err := r.Dispatch(ctx, line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", line, want, err)
		}
	}
	// Headless without a file: the usage.
	if err := r.Dispatch(ctx, "open"); err == nil || err.Error() != "open: a file is required without UI; usage: open [-t type] [-o name=value]... [file]" {
		t.Errorf("open without UI: %v", err)
	}
}

// TestOpenInteractive: file dialog, type (only when ambiguous), settings,
// grid — and every cancel point.
func TestOpenInteractive(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "a.csv")
	os.WriteFile(csvPath, []byte("x;y\n1;2\n"), 0o600)
	datPath := filepath.Join(dir, "a.dat")
	os.WriteFile(datPath, []byte("x,y\n1,2\n"), 0o600)
	ctx := context.Background()

	h := &openHost{openPath: csvPath}
	h.ui, h.formOK = true, true
	h.formSet = map[string]any{"delimiter": ";"}
	r := filesRegistry(t, h)
	if err := r.Dispatch(ctx, "open -o header=no"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.calls, ","); got != "openfile,form" {
		t.Errorf("order: %s", got)
	}
	if strings.Join(h.saveOpts.Extensions, ",") != "csv,tsv,txt" || h.formIn["header"] != false || !strings.HasPrefix(h.formTitle, "Open a.csv as CSV") {
		t.Errorf("dialogs: %+v %v %q", h.saveOpts, h.formIn, h.formTitle)
	}
	if res := shown(h); res == nil || rowsOf(res) != "x|y\n1|2" {
		t.Errorf("grid: %v", res)
	}
	// An unknown extension asks for the type.
	h = &openHost{openPath: datPath}
	h.ui, h.formOK, h.pick = true, true, "CSV (comma-separated values)"
	r = filesRegistry(t, h)
	if err := r.Dispatch(ctx, "open"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.calls, ","); got != "openfile,pick:Open as,form" {
		t.Errorf("order with pick: %s", got)
	}
	// Cancel points.
	for _, c := range []struct {
		name  string
		h     *openHost
		calls string
	}{
		{"file", &openHost{}, "openfile"},
		{"type", &openHost{openPath: datPath}, "openfile,pick:Open as"},
		{"settings", &openHost{openPath: csvPath}, "openfile,form"},
	} {
		c.h.ui = true
		r := filesRegistry(t, c.h)
		if err := r.Dispatch(ctx, "open"); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if got := strings.Join(c.h.calls, ","); got != c.calls || len(c.h.results) != 0 {
			t.Errorf("%s: calls %s, %d results", c.name, got, len(c.h.results))
		}
		var msgs []string
		for _, m := range c.h.messages {
			msgs = append(msgs, m.Info)
		}
		if strings.Join(msgs, "\n") != "open cancelled" {
			t.Errorf("%s: messages %q", c.name, msgs)
		}
	}
	// help open lists the types.
	h = &openHost{}
	r = filesRegistry(t, h)
	if err := r.Dispatch(ctx, "help open"); err != nil || len(h.texts) != 1 ||
		!strings.Contains(h.texts[0], "type csv — CSV (comma-separated values) (.csv, .tsv, .txt)") ||
		!strings.Contains(h.texts[0], "-o delimiter=<choice>  Delimiter (default: auto (detect)); auto, comma, semicolon, tab, pipe") {
		t.Errorf("help open: %v %q", err, h.texts)
	}
}

// TestCatShowsDecimalComma: cat file: names the decimal comma of a column.
func TestCatShowsDecimalComma(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "de.csv")
	os.WriteFile(p, []byte("betrag;text\n1.234,50;a\n-3,5;b\n"), 0o600)
	h := &openHost{}
	// A registry that remembers settings per file, as the UI does: open
	// with -o, then cat uses the same settings.
	r2, _ := newRegistry(t, nil)
	rt := script.New(script.Env{Workspace: r2.Runtime().Env().Workspace, Host: h, Files: script.NewFiles(0, 0, true)})
	t.Cleanup(rt.Close)
	reg := New(rt)
	if err := reg.LoadDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := reg.Dispatch(context.Background(), "open -o delimiter=semicolon -o decimal=comma "+p); err != nil {
		t.Fatal(err)
	}
	if err := reg.Dispatch(context.Background(), "cat file:"+p); err != nil || len(h.texts) != 1 {
		t.Fatalf("cat: %v %q", err, h.texts)
	}
	for _, want := range []string{`decimal=","`, "1 | betrag | yes, decimal comma", "2 | text   | no"} {
		if !strings.Contains(h.texts[0], want) {
			t.Errorf("cat lacks %q:\n%s", want, h.texts[0])
		}
	}
}
