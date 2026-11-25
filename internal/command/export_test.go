package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/script"
	"dbcc/internal/script/commands"
	"dbcc/internal/session"
)

// dialogHost is an interactive host with scripted answers; it records the
// dialogs in the order they were opened.
type dialogHost struct {
	host
	res       *exec.Result
	ui        bool
	calls     []string
	pick      string // title to choose ("" cancels)
	pickItems []string
	formOK    bool
	formSet   map[string]any // values the owner changes in the form
	formIn    map[string]any // values the form was opened with
	formTitle string
	savePath  string // "" cancels
	saveOpts  script.SaveFileOptions
	confirm   bool
	confirmed string
}

func (h *dialogHost) Interactive() bool                       { return h.ui }
func (h *dialogHost) ActiveResult() (*exec.Result, exec.View) { return h.res, nil }
func (h *dialogHost) Pick(title string, items []string) (int, error) {
	h.calls = append(h.calls, "pick:"+title)
	h.pickItems = items
	for i, it := range items {
		if it == h.pick {
			return i, nil
		}
	}
	return -1, nil
}
func (h *dialogHost) Form(title string, fields []script.OptionSpec, values map[string]any) (map[string]any, bool, error) {
	h.calls = append(h.calls, "form")
	h.formTitle, h.formIn = title, values
	out := map[string]any{}
	for k, v := range values {
		out[k] = v
	}
	for k, v := range h.formSet {
		out[k] = v
	}
	return out, h.formOK, nil
}
func (h *dialogHost) SaveFile(o script.SaveFileOptions) (string, bool, error) {
	h.calls = append(h.calls, "save")
	h.saveOpts = o
	return h.savePath, h.savePath != "", nil
}
func (h *dialogHost) Confirm(msg string) (bool, error) {
	h.calls = append(h.calls, "confirm")
	h.confirmed = msg
	return h.confirm, nil
}

func (h *dialogHost) infos() string {
	var out []string
	for _, m := range h.messages {
		out = append(out, m.Info)
	}
	return strings.Join(out, "\n")
}

func exportResult() *exec.Result {
	cols := []exec.ColumnInfo{{Name: "id", Numeric: true}, {Name: "name"}}
	return exec.NewResult("SELECT * FROM customers", cols, []exec.Row{{"1", "a;b"}, {"2", nil}, {"3", "c"}})
}

func newDialogRegistry(t *testing.T, h *dialogHost, scripts ...string) *Registry {
	t.Helper()
	cfg := &config.Config{Profiles: map[string]*config.Profile{}}
	rt := script.New(script.Env{Workspace: session.NewWorkspace(cfg, nil), Host: h})
	t.Cleanup(rt.Close)
	r := New(rt)
	if len(scripts) == 0 {
		if err := r.LoadDefaults(); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, n := range scripts {
		b, err := commands.FS.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rt.Load("embedded:"+n, string(b), true); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func fileText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestExportWithFile: with a file argument no dialog opens (headless works).
func TestExportWithFile(t *testing.T) {
	dir := t.TempDir()
	for _, ui := range []bool{true, false} {
		h := &dialogHost{res: exportResult(), ui: ui}
		r := newDialogRegistry(t, h)
		p := filepath.Join(dir, "a.csv")
		os.Remove(p)
		if err := r.Dispatch(context.Background(), "export "+p); err != nil {
			t.Fatal(err)
		}
		if got := fileText(t, p); got != "id,name\n1,a;b\n2,\n3,c\n" {
			t.Errorf("csv: %q", got)
		}
		if !strings.Contains(h.infos(), "export: 3 rows written to "+p+" (csv)") {
			t.Errorf("message: %q", h.infos())
		}
		// Type from -t, repeated -o, any extension.
		p2 := filepath.Join(dir, "b.txt")
		os.Remove(p2)
		if err := r.Dispatch(context.Background(), "export -t csv -o delimiter=';' -o header=false -o quoting=all "+p2); err != nil {
			t.Fatal(err)
		}
		if got := fileText(t, p2); got != "\"1\";\"a;b\"\n\"2\";\n\"3\";\"c\"\n" {
			t.Errorf("-t/-o: %q", got)
		}
		// Extension match is case-insensitive.
		p3 := filepath.Join(dir, "C.JSON")
		os.Remove(p3)
		if err := r.Dispatch(context.Background(), "export -o pretty=no "+p3); err != nil {
			t.Fatal(err)
		}
		if got := fileText(t, p3); got != `[{"id":"1","name":"a;b"},{"id":"2","name":null},{"id":"3","name":"c"}]`+"\n" {
			t.Errorf("json: %q", got)
		}
		if err := r.Dispatch(context.Background(), "export "+p); err == nil || !strings.Contains(err.Error(), "already exists; overwrite with export -f") {
			t.Errorf("overwrite: %v", err)
		}
		if err := r.Dispatch(context.Background(), "export -f "+p); err != nil {
			t.Errorf("-f: %v", err)
		}
		if len(h.calls) != 0 {
			t.Errorf("dialogs opened with a file argument: %v", h.calls)
		}
	}
}

func TestExportErrors(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ line, want string }{
		{"export " + filepath.Join(dir, "x.xlsx"), `export: unknown export type for "` + filepath.Join(dir, "x.xlsx") + `": use -t <type> or one of the extensions .csv, .json (types: csv, json)`},
		{"export -t xml " + filepath.Join(dir, "x.xml"), `export: unknown export type "xml" (types: csv, json)`},
		{"export -o header " + filepath.Join(dir, "x.csv"), `export: -o needs name=value, got "header"`},
		{"export -o sep=x " + filepath.Join(dir, "x.csv"), `unknown option "sep"`},
		{"export -o", `flag -o needs a string value`},
	}
	for _, c := range cases {
		h := &dialogHost{res: exportResult(), ui: true}
		r := newDialogRegistry(t, h)
		if err := r.Dispatch(context.Background(), c.line); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s:\n got  %v\n want %q", c.line, err, c.want)
		}
	}
	// Without a file and without UI: the usage, before anything else.
	h := &dialogHost{res: exportResult()}
	r := newDialogRegistry(t, h)
	if err := r.Dispatch(context.Background(), "export"); err == nil ||
		err.Error() != "export: a file is required without UI; usage: export [-f] [-t type] [-o name=value]... [file]" {
		t.Errorf("headless without file: %v", err)
	}
	// No active result: before any dialog.
	h = &dialogHost{ui: true}
	r = newDialogRegistry(t, h)
	if err := r.Dispatch(context.Background(), "export"); err == nil || !strings.Contains(err.Error(), "no active result to export") || len(h.calls) != 0 {
		t.Errorf("no result: %v %v", err, h.calls)
	}
}

// TestExportInteractive drives the flow without a file: type list,
// settings, file dialog, overwrite question, write.
func TestExportInteractive(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "old.csv")
	os.WriteFile(existing, []byte("old"), 0o600)
	h := &dialogHost{res: exportResult(), ui: true, pick: "CSV (comma-separated values)", formOK: true,
		formSet: map[string]any{"header": false}, savePath: existing, confirm: true}
	r := newDialogRegistry(t, h)
	if err := r.Dispatch(context.Background(), "export -o delimiter=semicolon"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.calls, ","); got != "pick:Export as,form,save,confirm" {
		t.Errorf("order: %s", got)
	}
	if strings.Join(h.pickItems, "|") != "CSV (comma-separated values)|JSON (array of objects)" {
		t.Errorf("type list: %q", h.pickItems)
	}
	// -o values pre-fill the form.
	if h.formTitle != "Export as CSV (comma-separated values)" || h.formIn["delimiter"] != ";" || h.formIn["header"] != true {
		t.Errorf("form: %q %v", h.formTitle, h.formIn)
	}
	if h.saveOpts.Name != "SELECT_FROM_customers.csv" || strings.Join(h.saveOpts.Extensions, ",") != "csv" ||
		!strings.HasPrefix(h.saveOpts.Title, "Export as CSV") {
		t.Errorf("save opts: %+v", h.saveOpts)
	}
	if h.confirmed != existing+" exists. Overwrite?" {
		t.Errorf("confirm: %q", h.confirmed)
	}
	if got := fileText(t, existing); got != "1;\"a;b\"\n2;\n3;c\n" {
		t.Errorf("written: %q", got)
	}
	if !strings.Contains(h.infos(), "export: 3 rows written to "+existing+" (csv)") {
		t.Errorf("message: %q", h.infos())
	}

	// -t skips the list; JSON has options too; a new file is not confirmed.
	p := filepath.Join(dir, "new.json")
	h = &dialogHost{res: exportResult(), ui: true, formOK: true, savePath: p}
	r = newDialogRegistry(t, h)
	if err := r.Dispatch(context.Background(), "export -t json"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.calls, ","); got != "form,save" {
		t.Errorf("order with -t: %s", got)
	}
	if got := fileText(t, p); !strings.HasPrefix(got, "[\n  {\"id\": \"1\"") {
		t.Errorf("json: %q", got)
	}

	// -f: no question.
	h = &dialogHost{res: exportResult(), ui: true, formOK: true, savePath: existing}
	r = newDialogRegistry(t, h)
	if err := r.Dispatch(context.Background(), "export -f -t csv"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.calls, ","); got != "form,save" {
		t.Errorf("order with -f: %s", got)
	}

	// With a single export type the list is skipped.
	h = &dialogHost{res: exportResult(), ui: true, formOK: true, savePath: filepath.Join(dir, "one.csv")}
	r = newDialogRegistry(t, h, "export.lua", "exporter_csv.lua")
	if err := r.Dispatch(context.Background(), "export"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.calls, ","); got != "form,save" {
		t.Errorf("single type: %s", got)
	}
}

func TestExportInteractiveCancel(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "old.csv")
	cases := []struct {
		name  string
		h     *dialogHost
		calls string
	}{
		{"type list", &dialogHost{}, "pick:Export as"},
		{"settings", &dialogHost{pick: "CSV (comma-separated values)"}, "pick:Export as,form"},
		{"file", &dialogHost{pick: "CSV (comma-separated values)", formOK: true}, "pick:Export as,form,save"},
		{"overwrite", &dialogHost{pick: "CSV (comma-separated values)", formOK: true, savePath: existing}, "pick:Export as,form,save,confirm"},
	}
	for _, c := range cases {
		os.WriteFile(existing, []byte("old"), 0o600)
		c.h.res, c.h.ui = exportResult(), true
		r := newDialogRegistry(t, c.h)
		if err := r.Dispatch(context.Background(), "export"); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if got := strings.Join(c.h.calls, ","); got != c.calls {
			t.Errorf("%s: calls %s, want %s", c.name, got, c.calls)
		}
		if c.h.infos() != "export cancelled" {
			t.Errorf("%s: messages %q", c.name, c.h.infos())
		}
		if fileText(t, existing) != "old" {
			t.Errorf("%s: file changed", c.name)
		}
	}
}

// TestHelpExportListsTypes: help export shows the registered types.
func TestHelpExportListsTypes(t *testing.T) {
	h := &dialogHost{}
	r := newDialogRegistry(t, h)
	if err := r.Dispatch(context.Background(), "help export"); err != nil {
		t.Fatal(err)
	}
	if len(h.texts) != 1 {
		t.Fatalf("texts %q", h.texts)
	}
	for _, want := range []string{"usage: export [-f] [-t type] [-o name=value]... [file]",
		"type csv — CSV (comma-separated values) (.csv)",
		"-o delimiter=<choice>  Delimiter (default: comma  ,); comma, semicolon, tab, pipe",
		"-o header=<bool>  Header row (default: true)",
		"type json — JSON (array of objects) (.json)", "-o pretty=<bool>"} {
		if !strings.Contains(h.texts[0], want) {
			t.Errorf("help export lacks %q:\n%s", want, h.texts[0])
		}
	}
}
