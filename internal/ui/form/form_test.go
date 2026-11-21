package form

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	driver "github.com/go-sql-driver/mysql"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
)

type call struct {
	q    string
	args []any
}

// fakeBackend is a synchronous Backend over the real MySQL dialect.
type fakeBackend struct {
	mu       sync.Mutex
	d        dialect.Dialect
	tables   map[string]*meta.Table
	execs    []call
	grids    []call
	queries  []call
	execErr  error
	affected int64
	gridRes  func(q string, args []any) *exec.Result
	queryRes *exec.Result
}

func newBackend() *fakeBackend {
	return &fakeBackend{d: my, affected: 1, tables: map[string]*meta.Table{"parent": parentTable(), "items": itemsTable()}}
}

func (b *fakeBackend) Exec(q string, args ...any) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.execs = append(b.execs, call{q, args})
	if b.execErr != nil {
		return 0, b.execErr
	}
	return b.affected, nil
}

func (b *fakeBackend) Query(q string, args ...any) (*exec.Result, error) {
	b.queries = append(b.queries, call{q, args})
	if b.queryRes == nil {
		return exec.NewResult("", nil, nil), nil
	}
	return b.queryRes, nil
}

func (b *fakeBackend) Grid(q string, args ...any) (*exec.Result, error) {
	b.grids = append(b.grids, call{q, args})
	if b.gridRes != nil {
		return b.gridRes(q, args), nil
	}
	return parentRows(), nil
}

func (b *fakeBackend) Dialect() dialect.Dialect { return b.d }

func (b *fakeBackend) Describe(p meta.Path) (*meta.Table, error) {
	if t, ok := b.tables[p.Object]; ok {
		return t, nil
	}
	return nil, meta.ErrNotFound
}

func parentRows() *exec.Result {
	return exec.NewResult("parent", []exec.ColumnInfo{{Name: "a", Numeric: true}, {Name: "b"}, {Name: "label"}},
		[]exec.Row{{"1", "a", "p1"}, {"2", "b", nil}, {"3", "c", "third"}})
}

// harness draws a form on a simulation screen.
type harness struct {
	t      *testing.T
	f      *Form
	be     *fakeBackend
	scr    tcell.SimulationScreen
	closed []bool
	msgs   []exec.Message
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	h := &harness{t: t, be: newBackend()}
	if opts.Table == nil {
		opts.Table = itemsTable()
	}
	if opts.Backend == nil {
		opts.Backend = h.be
	}
	opts.OnClose = func(saved bool) { h.closed = append(h.closed, saved) }
	opts.OnMessage = func(m exec.Message) { h.msgs = append(h.msgs, m) }
	h.f = New(opts)
	h.scr = tcell.NewSimulationScreen("UTF-8")
	if err := h.scr.Init(); err != nil {
		t.Fatal(err)
	}
	h.scr.SetSize(140, 40)
	h.f.SetRect(0, 0, 140, 40)
	h.f.Focus(nil)
	h.draw()
	return h
}

func (h *harness) draw() string {
	h.scr.Clear()
	h.f.Draw(h.scr)
	h.scr.Show()
	cells, w, ht := h.scr.GetContents()
	var b strings.Builder
	for y := 0; y < ht; y++ {
		for x := 0; x < w; x++ {
			c := cells[y*w+x]
			if len(c.Bytes) == 0 {
				b.WriteByte(' ')
			} else {
				b.Write(c.Bytes)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// line returns the screen line of column col's field.
func (h *harness) line(col string) string {
	for _, l := range strings.Split(h.draw(), "\n") {
		fs := strings.Fields(strings.TrimLeft(l, "║│ ▶*∅!"))
		if len(fs) > 0 && fs[0] == col {
			return l
		}
	}
	h.t.Fatalf("no line for %s:\n%s", col, h.draw())
	return ""
}

func (h *harness) key(k tcell.Key) {
	h.t.Helper()
	h.f.HandleKey(tcell.NewEventKey(k, 0, tcell.ModNone))
	h.draw()
}
func (h *harness) ctrl(r rune) {
	h.t.Helper()
	h.f.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModCtrl))
	h.draw()
}
func (h *harness) typ(s string) {
	h.t.Helper()
	for _, r := range s {
		h.f.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	h.draw()
}

func (h *harness) field(col string) FieldInfo {
	h.t.Helper()
	for _, fi := range h.f.Fields() {
		if fi.Column == col {
			return fi
		}
	}
	h.t.Fatalf("no field %s", col)
	return FieldInfo{}
}

func (h *harness) goTo(col string) {
	h.t.Helper()
	if !h.f.FocusField(col) {
		h.t.Fatalf("no field %s", col)
	}
	h.draw()
}

func TestStateTransitions(t *testing.T) {
	h := newHarness(t, Options{})
	if h.f.Current() != "code" {
		t.Fatalf("first editable field: %s", h.f.Current())
	}
	if l := h.line("id"); !strings.Contains(l, "auto") || !strings.Contains(l, "int unsigned") || !strings.Contains(l, "PK NN") {
		t.Fatalf("id line: %q", l)
	}
	if l := h.line("region"); !strings.Contains(l, "default 'EU'") {
		t.Fatalf("region line: %q", l)
	}

	// Typing sets a value.
	h.typ("abc")
	if fi := h.field("code"); fi.State != Value || fi.Value != "abc" {
		t.Fatalf("after typing: %+v", fi)
	}
	if l := h.line("code"); !strings.Contains(l, "*") || !strings.Contains(l, "abc") {
		t.Fatalf("value marker: %q", l)
	}
	// Ctrl+N: NULL.
	h.ctrl('n')
	if fi := h.field("code"); fi.State != Null {
		t.Fatalf("after ctrl+n: %+v", fi)
	}
	if l := h.line("code"); !strings.Contains(l, "∅") || !strings.Contains(l, "NULL") {
		t.Fatalf("null marker: %q", l)
	}
	// Ctrl+U: untouched.
	h.ctrl('u')
	if fi := h.field("code"); fi.State != Untouched || fi.Value != "" {
		t.Fatalf("after ctrl+u: %+v", fi)
	}
	if l := h.line("code"); strings.Contains(l, "∅") || strings.Contains(l, "*") || !strings.Contains(l, "no default") {
		t.Fatalf("untouched: %q", l)
	}
	// Empty string is a value, distinct from NULL and untouched.
	h.typ("x")
	h.key(tcell.KeyBackspace2)
	if fi := h.field("code"); fi.State != Value || fi.Value != "" {
		t.Fatalf("empty string: %+v", fi)
	}
	// Typing into a NULL field starts a value.
	h.ctrl('n')
	h.typ("z")
	if fi := h.field("code"); fi.State != Value || fi.Value != "z" {
		t.Fatalf("null then type: %+v", fi)
	}

	// Navigation: Down/Tab skip to the next field; auto fields are read-only.
	h.key(tcell.KeyDown)
	if h.f.Current() != "region" {
		t.Fatalf("down: %s", h.f.Current())
	}
	h.goTo("id")
	h.typ("9")
	h.ctrl('n')
	if fi := h.field("id"); fi.State != Untouched {
		t.Fatalf("auto field changed: %+v", fi)
	}

	// Bool: Space toggles, starting from the default 0.
	h.goTo("small")
	h.typ(" ")
	if fi := h.field("small"); fi.State != Value || fi.Value != "1" {
		t.Fatalf("bool: %+v", fi)
	}
	if l := h.line("small"); !strings.Contains(l, "[x]") {
		t.Fatalf("bool line %q", l)
	}
	h.typ(" ")
	if fi := h.field("small"); fi.Value != "0" {
		t.Fatalf("bool toggle back: %+v", fi)
	}

	// Set: Right moves between members, Space toggles.
	h.goTo("tags")
	h.typ(" ")
	h.key(tcell.KeyRight)
	h.key(tcell.KeyRight)
	h.typ(" ")
	if fi := h.field("tags"); fi.State != Value || fi.Value != "a,c" {
		t.Fatalf("set: %+v", fi)
	}
	if l := h.line("tags"); !strings.Contains(l, "[x] a") || !strings.Contains(l, "[ ] b") || !strings.Contains(l, "[x] c") {
		t.Fatalf("set line %q", l)
	}

	// Enum: Enter opens the drop-down, Down + Enter selects.
	h.goTo("status")
	h.key(tcell.KeyEnter)
	if h.f.popup == nil {
		t.Fatal("drop-down not open")
	}
	scr := h.draw()
	if !strings.Contains(scr, "it's done") {
		t.Fatalf("drop-down not drawn:\n%s", scr)
	}
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter)
	if fi := h.field("status"); fi.State != Value || fi.Value != "active" {
		t.Fatalf("enum: %+v", fi)
	}

	// Long text: F4 opens the multi-line editor, F2 applies.
	h.goTo("doc")
	h.key(tcell.KeyF4)
	if _, ok := h.f.EditorText(); !ok {
		t.Fatal("editor not open")
	}
	h.typ("{")
	h.key(tcell.KeyEnter)
	h.typ("}")
	h.key(tcell.KeyF2)
	if fi := h.field("doc"); fi.State != Value || fi.Value != "{\n}" {
		t.Fatalf("editor: %+v", fi)
	}
	if l := h.line("doc"); !strings.Contains(l, "{ …") {
		t.Fatalf("preview %q", l)
	}
	if h.f.Closed() || len(h.be.execs) != 0 {
		t.Fatal("F2 in the editor must not save the form")
	}
	// Esc in the editor discards.
	h.key(tcell.KeyF4)
	h.typ("zzz")
	h.key(tcell.KeyEscape)
	if fi := h.field("doc"); fi.Value != "{\n}" || h.f.Closed() {
		t.Fatalf("editor discard: %+v", fi)
	}

	// Binary: not editable.
	h.goTo("data")
	h.ctrl('n')
	if fi := h.field("data"); fi.State != Untouched {
		t.Fatalf("binary changed: %+v", fi)
	}

	// Save inserts exactly the touched fields.
	h.key(tcell.KeyF2)
	if len(h.be.execs) != 1 {
		t.Fatalf("execs %v", h.be.execs)
	}
	c := h.be.execs[0]
	want := "INSERT INTO `d`.`items` (`code`, `status`, `tags`, `small`, `doc`) VALUES (?, ?, ?, ?, ?)"
	if c.q != want || !reflect.DeepEqual(c.args, []any{"z", "active", "a,c", false, "{\n}"}) {
		t.Fatalf("insert %q %#v", c.q, c.args)
	}
	if !h.f.Closed() || !reflect.DeepEqual(h.closed, []bool{true}) {
		t.Fatalf("closed %v", h.closed)
	}
	if len(h.msgs) == 0 || h.msgs[0].Stmt != want || !strings.Contains(h.msgs[0].Info, "'z'") {
		t.Fatalf("message log %+v", h.msgs)
	}
}

func TestCancelAsks(t *testing.T) {
	h := newHarness(t, Options{})
	h.key(tcell.KeyEscape)
	if !h.f.Closed() || !reflect.DeepEqual(h.closed, []bool{false}) {
		t.Fatal("unchanged form closes on Esc")
	}
	h = newHarness(t, Options{})
	h.typ("x")
	h.key(tcell.KeyEscape)
	if h.f.Closed() || !strings.Contains(h.draw(), "Discard changes?") {
		t.Fatal("must ask")
	}
	h.typ("n")
	if h.f.Closed() {
		t.Fatal("n keeps the form")
	}
	h.key(tcell.KeyEscape)
	h.typ("y")
	if !h.f.Closed() {
		t.Fatal("y closes")
	}
}

func TestSaveNewAndAllDefaults(t *testing.T) {
	h := newHarness(t, Options{})
	h.key(tcell.KeyF6)
	if len(h.be.execs) != 1 || h.be.execs[0].q != "INSERT INTO `d`.`items` () VALUES ()" {
		t.Fatalf("defaults insert %v", h.be.execs)
	}
	if h.f.Closed() || h.f.Mode() != ModeNew || !strings.Contains(h.f.Message(), "Saved") {
		t.Fatalf("save+new: closed=%v msg=%q", h.f.Closed(), h.f.Message())
	}
	h.typ("q")
	h.key(tcell.KeyF2)
	if len(h.be.execs) != 2 || h.be.execs[1].q != "INSERT INTO `d`.`items` (`code`) VALUES (?)" || !h.f.Closed() {
		t.Fatalf("second insert %v", h.be.execs)
	}
}

func editRow() ([]exec.ColumnInfo, exec.Row) {
	tbl := itemsTable()
	var cols []exec.ColumnInfo
	var row exec.Row
	vals := map[string]any{"id": "5", "code": "c1", "region": "EU", "name": "Alpha", "note": nil, "price": "12.50", "qty": "2",
		"total": "25.00", "status": "active", "tags": "a,b", "created": "2024-01-02 03:04:05", "small": "1", "flag": "\x01",
		"data": exec.Binary{1, 2}, "bin": nil, "doc": `{"k": 1}`, "body": nil, "parent_a": "1", "parent_b": "a"}
	for _, c := range tbl.Columns {
		cols = append(cols, exec.ColumnInfo{Name: c.Name})
		row = append(row, vals[c.Name])
	}
	return cols, row
}

func TestEditUpdate(t *testing.T) {
	cols, row := editRow()
	h := newHarness(t, Options{Mode: ModeEdit, Columns: cols, Row: row, BackendLogs: true})
	h.be.queryRes = exec.NewResult("", []exec.ColumnInfo{{Name: "label"}}, []exec.Row{{"p1"}})
	// The FK description query ran at open (synchronous Async) against the
	// fake before queryRes was set; reopen with it in place.
	be := h.be
	h = newHarness(t, Options{Mode: ModeEdit, Columns: cols, Row: row, BackendLogs: true, Backend: be})
	if len(be.queries) != 2 || be.queries[1].q != "SELECT `label` FROM `d`.`parent` WHERE `a` = ? AND `b` = ?" ||
		!reflect.DeepEqual(be.queries[1].args, []any{"1", "a"}) {
		t.Fatalf("fk description query %v", be.queries)
	}
	if l := h.line("parent_a"); !strings.Contains(l, "1 · p1") {
		t.Fatalf("fk display %q", l)
	}
	if l := h.line("id"); !strings.Contains(l, "5  (auto)") {
		t.Fatalf("id %q", l)
	}
	if l := h.line("note"); !strings.Contains(l, "NULL") {
		t.Fatalf("note %q", l)
	}
	if !strings.Contains(h.draw(), " Edit d.items (id=5) ") {
		t.Fatalf("title:\n%s", h.draw())
	}
	// Edit code (orig c1): append "x"; set name NULL; type same small.
	h.goTo("code")
	h.typ("x")
	h.goTo("name")
	h.ctrl('n')
	h.goTo("note")
	h.typ("")
	h.goTo("region")
	h.key(tcell.KeyBackspace2)
	h.typ("U")
	h.key(tcell.KeyBackspace2)
	h.typ("U") // "EU" again: value but unchanged
	if fi := h.field("region"); fi.State != Value || fi.Value != "EU" {
		t.Fatalf("region %+v", fi)
	}
	// Ctrl+U restores the original.
	h.goTo("qty")
	h.typ("9")
	h.ctrl('u')
	if fi := h.field("qty"); fi.State != Untouched || !strings.Contains(h.line("qty"), " 2") {
		t.Fatalf("qty %+v %q", fi, h.line("qty"))
	}
	h.key(tcell.KeyF2)
	if len(be.execs) != 1 {
		t.Fatalf("execs %v", be.execs)
	}
	if c := be.execs[0]; c.q != "UPDATE `d`.`items` SET `code` = ?, `name` = ? WHERE `id` = ?" || !reflect.DeepEqual(c.args, []any{"c1x", nil, "5"}) {
		t.Fatalf("update %q %#v", c.q, c.args)
	}
	if !h.f.Closed() || !reflect.DeepEqual(h.closed, []bool{true}) {
		t.Fatal("closed after update")
	}
	// BackendLogs: only the parameters are logged.
	if len(h.msgs) != 1 || h.msgs[0].Stmt != "" || !strings.Contains(h.msgs[0].Info, "'c1x', NULL, '5'") {
		t.Fatalf("msgs %+v", h.msgs)
	}
}

func TestEditAffectedNotOne(t *testing.T) {
	cols, row := editRow()
	h := newHarness(t, Options{Mode: ModeEdit, Columns: cols, Row: row})
	h.be.affected = 0
	h.goTo("code")
	h.typ("x")
	h.key(tcell.KeyF2)
	if h.f.Closed() {
		t.Fatal("must stay open")
	}
	if !strings.Contains(h.draw(), "WARNING: UPDATE affected 0 rows, expected 1") {
		t.Fatalf("warning missing:\n%s", h.draw())
	}
	var warned bool
	for _, m := range h.msgs {
		if m.Err != nil && strings.Contains(m.Err.Error(), "affected 0 rows") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no log warning: %+v", h.msgs)
	}
	h.key(tcell.KeyEscape) // saved: no question
	if !h.f.Closed() || !reflect.DeepEqual(h.closed, []bool{true}) {
		t.Fatal("Esc after save closes")
	}
}

func TestEditRefused(t *testing.T) {
	nokey := &meta.Table{Path: meta.Path{Object: "logs"}, Columns: []meta.Column{{Name: "msg", DataType: "varchar(9)"}}}
	h := newHarness(t, Options{Table: nokey, Mode: ModeEdit, Columns: []exec.ColumnInfo{{Name: "msg"}}, Row: exec.Row{"x"}})
	if h.f.Refusal() == "" || !strings.Contains(h.draw(), "Cannot edit") {
		t.Fatal("refusal not shown")
	}
	h.typ("y")
	h.key(tcell.KeyF2)
	if len(h.be.execs) != 0 || h.f.Closed() {
		t.Fatal("must not save")
	}
}

func myErr(num uint16, msg string) error {
	return fmt.Errorf("exec: %w", &driver.MySQLError{Number: num, Message: msg})
}

func TestErrorMapping(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		cols []string
	}{
		{"not null", myErr(1048, "Column 'code' cannot be null"), []string{"code"}},
		{"duplicate", myErr(1062, "Duplicate entry 'c1-EU' for key 'items.uq_code_region'"), []string{"code", "region"}},
		{"duplicate pk", myErr(1062, "Duplicate entry '1' for key 'PRIMARY'"), []string{"id"}},
		{"fk", myErr(1452, "Cannot add or update a child row: a foreign key constraint fails (`d`.`items`, CONSTRAINT `fk_items_parent` FOREIGN KEY (`parent_a`, `parent_b`) REFERENCES `parent` (`a`, `b`) ON DELETE CASCADE ON UPDATE RESTRICT)"), []string{"parent_a", "parent_b"}},
		{"check mysql", myErr(3819, "Check constraint 'chk_price' is violated."), []string{"price"}},
		{"check mysql column", myErr(3819, "Check constraint 'items_chk_1' is violated."), []string{"qty"}},
		{"check mariadb", myErr(4025, "CONSTRAINT `qty` failed for `d`.`items`"), []string{"qty"}},
		{"check mariadb table", myErr(4025, "CONSTRAINT `chk_price` failed for `d`.`items`"), []string{"price"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Options{})
			h.be.execErr = c.err
			h.typ("c1")
			h.key(tcell.KeyF2)
			if h.f.Closed() {
				t.Fatal("form must stay open on error")
			}
			var marked []string
			for _, fi := range h.f.Fields() {
				if fi.Error != "" {
					marked = append(marked, fi.Column)
				}
			}
			if !reflect.DeepEqual(marked, c.cols) {
				t.Fatalf("marked %v, want %v", marked, c.cols)
			}
			if h.f.Current() != c.cols[0] {
				t.Fatalf("focus %s", h.f.Current())
			}
			var me *driver.MySQLError
			errors.As(c.err, &me)
			scr := h.draw()
			if !strings.Contains(h.f.Message(), me.Message) || !strings.Contains(scr, clip(me.Message, 60)[:20]) {
				t.Fatalf("message %q\n%s", h.f.Message(), scr)
			}
			if l := h.line(c.cols[0]); !strings.Contains(l, "!") {
				t.Fatalf("no error marker: %q\n%s", l, scr)
			}
			if len(h.msgs) != 1 || h.msgs[0].Err == nil {
				t.Fatalf("statement with error not logged: %+v", h.msgs)
			}
			// A later successful save clears the marks and closes.
			h.be.execErr = nil
			h.key(tcell.KeyF2)
			if !h.f.Closed() {
				t.Fatal("second save")
			}
		})
	}
	// Errors MapError does not know are shown as they are.
	h := newHarness(t, Options{})
	h.be.execErr = fmt.Errorf("connection lost")
	h.typ("x")
	h.key(tcell.KeyF2)
	if h.f.Closed() || !strings.Contains(h.f.Message(), "connection lost") {
		t.Fatalf("plain error: %q", h.f.Message())
	}
}

func TestCompositeFKLookup(t *testing.T) {
	h := newHarness(t, Options{LookupLimit: 50})
	h.goTo("parent_b")
	h.typ("7") // typing on an FK field does nothing
	if fi := h.field("parent_b"); fi.State != Untouched {
		t.Fatalf("fk typed: %+v", fi)
	}
	h.key(tcell.KeyF4)
	if h.f.look == nil {
		t.Fatal("lookup not open")
	}
	if len(h.be.grids) != 1 || h.be.grids[0].q != "SELECT * FROM (SELECT * FROM `d`.`parent`) AS dbcc_limit LIMIT 50" {
		t.Fatalf("lookup query %v", h.be.grids)
	}
	scr := h.draw()
	for _, s := range []string{"fk_items_parent → parent (a, b)", "Filter:", "label", "third", "Enter select"} {
		if !strings.Contains(scr, s) {
			t.Fatalf("lookup screen lacks %q:\n%s", s, scr)
		}
	}
	// Filtering re-queries server-side, binding the text once per searchable column.
	h.be.gridRes = func(q string, args []any) *exec.Result {
		return exec.NewResult("parent", []exec.ColumnInfo{{Name: "a"}, {Name: "b"}, {Name: "label"}}, []exec.Row{{"3", "c", "third"}})
	}
	h.typ("th")
	if len(h.be.grids) != 3 {
		t.Fatalf("filter queries %d", len(h.be.grids))
	}
	g := h.be.grids[2]
	wantQ := "SELECT * FROM (SELECT * FROM `d`.`parent` WHERE (LOCATE(LOWER(?), LOWER(CAST(`a` AS CHAR))) > 0 OR LOCATE(LOWER(?), LOWER(`b`)) > 0 OR LOCATE(LOWER(?), LOWER(`label`)) > 0)) AS dbcc_limit LIMIT 50"
	if g.q != wantQ || !reflect.DeepEqual(g.args, []any{"th", "th", "th"}) {
		t.Fatalf("filter query\n%s\n%v", g.q, g.args)
	}
	h.key(tcell.KeyEnter)
	if h.f.look != nil {
		t.Fatal("lookup still open")
	}
	a, b := h.field("parent_a"), h.field("parent_b")
	if a.State != Value || a.Value != "3" || b.State != Value || b.Value != "c" || a.Desc != "third" || b.Desc != "third" {
		t.Fatalf("fk fields %+v %+v", a, b)
	}
	if l := h.line("parent_a"); !strings.Contains(l, "3 · third") {
		t.Fatalf("fk display %q", l)
	}
	// Ctrl+N on one FK column sets the whole key NULL; Ctrl+U resets both.
	h.ctrl('n')
	if h.field("parent_a").State != Null || h.field("parent_b").State != Null {
		t.Fatal("fk null")
	}
	h.ctrl('u')
	if h.field("parent_a").State != Untouched || h.field("parent_b").State != Untouched {
		t.Fatal("fk untouch")
	}
	// Pick again (second row of the unfiltered list) and save.
	h.be.gridRes = nil
	h.key(tcell.KeyF4)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter)
	if a, b := h.field("parent_a"), h.field("parent_b"); a.Value != "2" || b.Value != "b" || a.Desc != "" {
		t.Fatalf("second pick %+v %+v", a, b)
	}
	// Esc closes the lookup without changes.
	h.key(tcell.KeyF4)
	h.key(tcell.KeyEscape)
	if h.f.look != nil || h.f.Closed() {
		t.Fatal("esc in lookup")
	}
	h.key(tcell.KeyF2)
	if c := h.be.execs[0]; c.q != "INSERT INTO `d`.`items` (`parent_a`, `parent_b`) VALUES (?, ?)" || !reflect.DeepEqual(c.args, []any{"2", "b"}) {
		t.Fatalf("insert %q %v", c.q, c.args)
	}
}

// TestAsync checks that blocking calls run inside Async jobs and results
// are applied only in done.
func TestAsync(t *testing.T) {
	var jobs []func()
	var dones []func()
	h := newHarness(t, Options{Async: func(job, done func()) { jobs = append(jobs, job); dones = append(dones, done) }})
	h.typ("x")
	h.key(tcell.KeyF2)
	if len(h.be.execs) != 0 || !h.f.Busy() || len(jobs) != 1 {
		t.Fatal("exec must wait for the worker")
	}
	h.typ("more") // ignored while busy
	jobs[0]()
	if h.f.Closed() {
		t.Fatal("done not yet run")
	}
	dones[0]()
	if !h.f.Closed() || len(h.be.execs) != 1 || !reflect.DeepEqual(h.be.execs[0].args, []any{"x"}) {
		t.Fatalf("after done: %v", h.be.execs)
	}
}
