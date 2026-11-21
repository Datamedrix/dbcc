package grid

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"

	"dbcc/internal/dialect/mysql"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
)

var custCols = []exec.ColumnInfo{
	{Name: "id", DBType: "INT", Numeric: true},
	{Name: "name", DBType: "VARCHAR"},
	{Name: "city", DBType: "VARCHAR"},
}

func custRows() []exec.Row {
	return []exec.Row{
		{"1", "Bob", "Berlin"},
		{"2", "alice", "Paris"},
		{"3", "Carol", "Bern"},
		{"10", "dave", nil},
	}
}

func custTable() *meta.Table {
	return &meta.Table{
		Path: meta.Path{Database: "shop", Object: "customers"},
		Columns: []meta.Column{
			{Name: "id", DataType: "int"}, {Name: "name", DataType: "varchar(40)"}, {Name: "city", DataType: "varchar(40)"},
		},
		PrimaryKey: &meta.Key{Name: "PRIMARY", Columns: []string{"id"}},
	}
}

func originResult(rows []exec.Row) *exec.Result {
	res := exec.NewResult("customers", custCols, rows)
	res.Origin = &exec.Origin{Object: meta.Path{Database: "shop", Object: "customers"}, SQL: "`shop`.`customers`", Table: custTable()}
	return res
}

func runeKey(g tview.Primitive, r rune) {
	g.InputHandler()(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone), func(p tview.Primitive) { p.Focus(func(tview.Primitive) {}) })
}

func keyP(p tview.Primitive, k tcell.Key) {
	p.InputHandler()(tcell.NewEventKey(k, 0, tcell.ModNone), func(q tview.Primitive) { q.Focus(func(tview.Primitive) {}) })
}

func typeText(p tview.Primitive, s string) {
	for _, r := range s {
		runeKey(p, r)
	}
}

// column returns the displayed values of column col (normal view).
func column(g *Grid, col int) []any {
	var out []any
	for i := 0; i < g.content.rows(); i++ {
		out = append(out, g.content.row(i)[col])
	}
	return out
}

func TestSortCycleAndHeaderMarkers(t *testing.T) {
	g := New(exec.NewResult("t", custCols, custRows()), Options{})
	s := newScreen(t, 80, 8)
	key(g, tcell.KeyRight) // name
	if !g.Action("sort") {
		t.Fatal("sort not handled")
	}
	draw(s, g)
	if hdr := line(s, 0); !strings.Contains(hdr, "name ▲1") {
		t.Fatalf("header %q", hdr)
	}
	if got := column(g, 1); !reflect.DeepEqual(got, []any{"Bob", "Carol", "alice", "dave"}) {
		t.Fatalf("asc = %v", got)
	}
	if st := g.Status(); !strings.Contains(st, "sort: name ▲") || !strings.Contains(st, "sorted within 4 loaded rows") {
		t.Fatalf("status %q", st)
	}
	if g.CurrentColumn() != 1 {
		t.Fatalf("cursor column moved: %d", g.CurrentColumn())
	}
	g.Action("sort")
	draw(s, g)
	if hdr := line(s, 0); !strings.Contains(hdr, "name ▼1") {
		t.Fatalf("header %q", hdr)
	}
	if got := column(g, 1); !reflect.DeepEqual(got, []any{"dave", "alice", "Carol", "Bob"}) {
		t.Fatalf("desc = %v", got)
	}
	g.Action("sort")
	draw(s, g)
	if hdr := line(s, 0); strings.Contains(hdr, "▲") || strings.Contains(hdr, "▼") {
		t.Fatalf("header still marked %q", hdr)
	}
	if got := column(g, 0); !reflect.DeepEqual(got, []any{"1", "2", "3", "10"}) {
		t.Fatalf("off = %v", got)
	}
	if st := g.Status(); st != "4 rows" {
		t.Fatalf("status %q", st)
	}
	if g.View() != nil {
		t.Fatal("View should be nil without local ops")
	}
}

func TestMultiKeySort(t *testing.T) {
	rows := []exec.Row{
		{"1", "b", "X"}, {"2", "a", "Y"}, {"3", "c", "X"}, {"4", "a", "X"},
	}
	g := New(exec.NewResult("t", custCols, rows), Options{})
	s := newScreen(t, 80, 8)
	key(g, tcell.KeyRight)
	key(g, tcell.KeyRight) // city
	g.Action("sort")
	key(g, tcell.KeyLeft) // name
	g.Action("sort_add")
	draw(s, g)
	hdr := line(s, 0)
	if !strings.Contains(hdr, "city ▲1") || !strings.Contains(hdr, "name ▲2") {
		t.Fatalf("header %q", hdr)
	}
	if got := column(g, 0); !reflect.DeepEqual(got, []any{"4", "1", "3", "2"}) {
		t.Fatalf("order = %v", got)
	}
	g.Action("sort_add") // cycles name in place
	draw(s, g)
	if hdr := line(s, 0); !strings.Contains(hdr, "name ▼2") || !strings.Contains(hdr, "city ▲1") {
		t.Fatalf("header %q", hdr)
	}
	if got := column(g, 0); !reflect.DeepEqual(got, []any{"3", "1", "4", "2"}) {
		t.Fatalf("order = %v", got)
	}
	// `s` on a column that is not a key replaces the sort.
	key(g, tcell.KeyLeft)
	g.Action("sort")
	if st := g.State(); !reflect.DeepEqual(st.Sort, []SortKey{{Col: 0}}) {
		t.Fatalf("sort = %+v", st.Sort)
	}
}

func TestLocalSortTypeAwareNullsFirst(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "n", Numeric: true}, {Name: "s"}}
	rows := []exec.Row{{"10", "b"}, {"9", nil}, {nil, "a"}, {"100", "B"}, {"-1.5", "aa"}}
	g := New(exec.NewResult("t", cols, rows), Options{})
	g.Action("sort")
	if got := column(g, 0); !reflect.DeepEqual(got, []any{nil, "-1.5", "9", "10", "100"}) {
		t.Fatalf("numeric asc = %v", got)
	}
	g.Action("sort")
	if got := column(g, 0); !reflect.DeepEqual(got, []any{"100", "10", "9", "-1.5", nil}) {
		t.Fatalf("numeric desc = %v", got)
	}
	g.Action("sort")
	key(g, tcell.KeyRight)
	g.Action("sort")
	if got := column(g, 1); !reflect.DeepEqual(got, []any{nil, "B", "a", "aa", "b"}) {
		t.Fatalf("string asc = %v", got)
	}
	// CurrentRecord maps through the view to Result().Rows().
	rec, ok := g.CurrentRecord()
	if !ok || rec != 1 {
		t.Fatalf("CurrentRecord = %d,%v", rec, ok)
	}
	if r, _ := g.CurrentRow(); r[1] != nil {
		t.Fatalf("CurrentRow = %v", r)
	}
}

func TestQueryBuilderSQL(t *testing.T) {
	d := &mysql.MySQL{}
	cols := append(slices.Clone(custCols), exec.ColumnInfo{Name: "we`ird", DBType: "BLOB", Binary: true})
	tbl := custTable()
	tbl.Columns = append(tbl.Columns, meta.Column{Name: "we`ird", DataType: "blob"})
	b := &QueryBuilder{Dialect: d, From: "`shop`.`customers`", Columns: cols, Table: tbl}
	search := "(LOCATE(LOWER(?), LOWER(CAST(`id` AS CHAR))) > 0 OR LOCATE(LOWER(?), LOWER(`name`)) > 0 OR LOCATE(LOWER(?), LOWER(`city`)) > 0)"
	cases := []struct {
		name string
		st   State
		sql  string
		args []any
	}{
		{"plain", State{}, "SELECT * FROM `shop`.`customers`", nil},
		{"sort", State{Sort: []SortKey{{Col: 1}}}, "SELECT * FROM `shop`.`customers` ORDER BY `name` ASC", nil},
		{"multi sort quoted", State{Sort: []SortKey{{Col: 2, Desc: true}, {Col: 3}}},
			"SELECT * FROM `shop`.`customers` ORDER BY `city` DESC, `we``ird` ASC", nil},
		{"search", State{Search: "Ber"}, "SELECT * FROM `shop`.`customers` WHERE " + search, []any{"Ber", "Ber", "Ber"}},
		{"filters", State{Filters: []ColumnFilter{
			{Col: 2, Op: OpStartsWith, Value: "B%_!"},
			{Col: 0, Op: OpGreaterEq, Value: "2"},
			{Col: 2, Op: OpNotNull},
			{Col: 3, Op: OpIsNull},
			{Col: 1, Op: OpContains, Value: "ar"},
			{Col: 0, Op: OpNotEquals, Value: "3"},
		}},
			"SELECT * FROM `shop`.`customers` WHERE `city` LIKE ? ESCAPE '!' AND `id` >= ? AND `city` IS NOT NULL AND `we``ird` IS NULL" +
				" AND (LOCATE(LOWER(?), LOWER(`name`)) > 0) AND `id` <> ?",
			[]any{"B!%!_!!%", "2", "ar", "3"}},
		{"all", State{
			Sort:    []SortKey{{Col: 0, Desc: true}},
			Search:  "x",
			Filters: []ColumnFilter{{Col: 1, Op: OpEquals, Value: "Bob"}, {Col: 0, Op: OpLess, Value: "10"}},
			Where:   "  city IN ('Bern', 'Berlin') OR id = 1 ",
		},
			"SELECT * FROM `shop`.`customers` WHERE " + search + " AND `name` = ? AND `id` < ? AND (city IN ('Bern', 'Berlin') OR id = 1) ORDER BY `id` DESC",
			[]any{"x", "x", "x", "Bob", "10"}},
		{"contains on binary", State{Filters: []ColumnFilter{{Col: 3, Op: OpContains, Value: "z"}}},
			"SELECT * FROM `shop`.`customers` WHERE 1 = 0", []any{}},
	}
	for _, c := range cases {
		sql, args := b.Build(c.st)
		if sql != c.sql {
			t.Errorf("%s:\n got %s\nwant %s", c.name, sql, c.sql)
		}
		if len(args) != len(c.args) || (len(args) > 0 && !reflect.DeepEqual(args, c.args)) {
			t.Errorf("%s: args %#v want %#v", c.name, args, c.args)
		}
	}

	// Without metadata the data types come from the result columns.
	b2 := NewQueryBuilder(d, &exec.Result{Columns: custCols, Origin: &exec.Origin{Object: meta.Path{Database: "shop", Object: "customers"}}})
	if sql, args := b2.Build(State{Search: "q", Sort: []SortKey{{Col: 1}}}); sql != "SELECT * FROM `shop`.`customers` WHERE "+search+" ORDER BY `name` ASC" || len(args) != 3 {
		t.Errorf("no metadata: %s %v", sql, args)
	}
	// Numbered placeholders that can repeat: the search text is bound once.
	b2.Dialect = numberedDialect{&mysql.MySQL{}}
	if sql, args := b2.Build(State{Search: "q", Filters: []ColumnFilter{{Col: 0, Op: OpEquals, Value: "1"}}}); !strings.Contains(sql, "LOWER($1)") ||
		!strings.HasSuffix(sql, "`id` = $2") || !reflect.DeepEqual(args, []any{"q", "1"}) {
		t.Errorf("numbered: %s %v", sql, args)
	}
}

// numberedDialect stands in for PostgreSQL: "$n" placeholders that bind
// the same argument at every occurrence.
type numberedDialect struct{ *mysql.MySQL }

func (numberedDialect) Placeholder(n int) string               { return "$" + strconv.Itoa(n) }
func (numberedDialect) TextSearchParamCount([]meta.Column) int { return 1 }

type requeryLog struct {
	sqls []string
	args [][]any
	err  error
	res  func() *exec.Result
}

func (l *requeryLog) fn(q string, args []any) (*exec.Result, error) {
	l.sqls = append(l.sqls, q)
	l.args = append(l.args, args)
	if l.err != nil {
		return nil, l.err
	}
	res := l.res()
	return res, nil
}

func TestServerSideSortAndFilter(t *testing.T) {
	log := &requeryLog{res: func() *exec.Result {
		r := custRows()
		slices.Reverse(r)
		return exec.NewResult("q", custCols, r) // Origin is carried over
	}}
	var changes int
	g := New(originResult(custRows()), Options{Dialect: &mysql.MySQL{}, Requery: log.fn, OnChange: func() { changes++ }})
	if !g.TableOrigin() {
		t.Fatal("not table-origin")
	}
	s := newScreen(t, 80, 8)
	key(g, tcell.KeyRight)
	key(g, tcell.KeyRight) // city
	g.Action("sort")
	if len(log.sqls) != 1 || log.sqls[0] != "SELECT * FROM `shop`.`customers` ORDER BY `city` ASC" {
		t.Fatalf("sqls = %q", log.sqls)
	}
	if g.CurrentColumn() != 2 {
		t.Fatalf("cursor column = %d", g.CurrentColumn())
	}
	res := g.Result()
	if res.Origin == nil || res.Origin.SQL != "`shop`.`customers`" {
		t.Fatal("origin not carried over")
	}
	if r, _ := g.CurrentCell(); r != 0 || res.Rows()[0][0] != "10" {
		t.Fatalf("new result not shown: row %d %v", r, res.Rows()[0])
	}
	draw(s, g)
	if hdr := line(s, 0); !strings.Contains(hdr, "city ▲1") {
		t.Fatalf("header %q", hdr)
	}
	if st := g.Status(); st != "4 rows  sort: city ▲" {
		t.Fatalf("status %q", st)
	}
	if g.View() != nil {
		t.Fatal("server-side results have no local view")
	}
	g.SetSearch("ber")
	g.AddFilter(ColumnFilter{Col: 0, Op: OpGreater, Value: "1"})
	g.SetWhere("name <> 'x'")
	want := "SELECT * FROM `shop`.`customers` WHERE (LOCATE(LOWER(?), LOWER(CAST(`id` AS CHAR))) > 0 OR LOCATE(LOWER(?), LOWER(`name`)) > 0 OR LOCATE(LOWER(?), LOWER(`city`)) > 0)" +
		" AND `id` > ? AND (name <> 'x') ORDER BY `city` ASC"
	if last := log.sqls[len(log.sqls)-1]; last != want {
		t.Fatalf("sql\n got %s\nwant %s", last, want)
	}
	if a := log.args[len(log.args)-1]; !reflect.DeepEqual(a, []any{"ber", "ber", "ber", "1"}) {
		t.Fatalf("args %v", a)
	}
	if st := g.Status(); !strings.Contains(st, `filter: search "ber" AND id > "1" AND (name <> 'x')`) {
		t.Fatalf("status %q", st)
	}
	if items := g.ActiveItems(); len(items) != 4 {
		t.Fatalf("items %q", items)
	}
	g.Remove(2) // the column filter
	if last := log.sqls[len(log.sqls)-1]; strings.Contains(last, "`id` >") || !strings.Contains(last, "(name <> 'x')") {
		t.Fatalf("after remove: %s", last)
	}
	if !g.Action("rerun") || len(log.sqls) != 6 {
		t.Fatalf("rerun: %d", len(log.sqls))
	}
	g.Action("clear_filters") // several → no OpenPrompt → all
	if last := log.sqls[len(log.sqls)-1]; last != "SELECT * FROM `shop`.`customers`" {
		t.Fatalf("after clear: %s", last)
	}
	if g.Action("rerun") {
		t.Fatal("rerun without state belongs to the app")
	}
	if changes == 0 {
		t.Fatal("OnChange not called")
	}
}

func TestRejectedWhereKeepsPreviousResult(t *testing.T) {
	log := &requeryLog{res: func() *exec.Result { return exec.NewResult("q", custCols, custRows()[:1]) }}
	var errs []error
	g := New(originResult(custRows()), Options{Dialect: &mysql.MySQL{}, Requery: log.fn, OnError: func(e error) { errs = append(errs, e) }})
	g.Action("sort")
	before := g.Result()
	key(g, tcell.KeyDown)
	log.err = errors.New("Error 1064: syntax error")
	g.SetWhere("id >>> 1")
	if len(errs) != 1 || errs[0] != log.err {
		t.Fatalf("errs = %v", errs)
	}
	if g.Result() != before {
		t.Fatal("result replaced after error")
	}
	if st := g.State(); st.Where != "" || len(st.Sort) != 1 {
		t.Fatalf("state changed: %+v", st)
	}
	if last := log.sqls[len(log.sqls)-1]; !strings.HasSuffix(last, "WHERE (id >>> 1) ORDER BY `id` ASC") {
		t.Fatalf("sql %s", last)
	}
	if g.Fetching() {
		t.Fatal("still busy")
	}
	// Free WHERE on a free result is refused.
	errs = nil
	f := New(exec.NewResult("t", custCols, custRows()), Options{Dialect: &mysql.MySQL{}, Requery: log.fn, OnError: func(e error) { errs = append(errs, e) }})
	if !f.Action("filter_where") || len(errs) != 1 || !errors.Is(errs[0], ErrWhereFreeResult) {
		t.Fatalf("errs = %v", errs)
	}
}

func TestRequeryAsyncThroughFetcherAndUI(t *testing.T) {
	log := &requeryLog{res: func() *exec.Result { return exec.NewResult("q", custCols, custRows()[:2]) }}
	var worker, ui []func()
	g := New(originResult(custRows()), Options{
		Dialect: &mysql.MySQL{}, Requery: log.fn,
		Fetcher: func(fn func()) { worker = append(worker, fn) },
		UI:      func(f func()) { ui = append(ui, f) },
	})
	g.Action("sort")
	if len(log.sqls) != 0 || len(worker) != 1 || !g.Fetching() {
		t.Fatalf("requery not deferred to the worker")
	}
	if st := g.Status(); !strings.Contains(st, "querying…") {
		t.Fatalf("status %q", st)
	}
	g.Action("sort") // busy
	if len(worker) != 1 {
		t.Fatal("second requery queued")
	}
	worker[0]()
	if len(ui) != 1 || g.Result().Len() != 4 {
		t.Fatal("result applied off the UI goroutine")
	}
	ui[0]()
	if g.Result().Len() != 2 || g.Fetching() || len(g.State().Sort) != 1 {
		t.Fatalf("not applied: len %d", g.Result().Len())
	}
	// A re-query that finishes after SetResult is dropped.
	g.Action("sort")
	other := exec.NewResult("o", custCols, nil)
	g.SetResult(other)
	worker[1]()
	ui[1]()
	if g.Result() != other {
		t.Fatal("stale re-query applied")
	}
}

func TestLocalFilters(t *testing.T) {
	g := New(exec.NewResult("t", custCols, custRows()), Options{})
	g.SetSearch("BER")
	if got := column(g, 0); !reflect.DeepEqual(got, []any{"1", "3"}) {
		t.Fatalf("search = %v", got)
	}
	if st := g.Status(); !strings.Contains(st, `filter: search "BER"`) || !strings.Contains(st, "filtered within 4 loaded rows (2 match)") {
		t.Fatalf("status %q", st)
	}
	g.AddFilter(ColumnFilter{Col: 1, Op: OpStartsWith, Value: "c"})
	if got := column(g, 0); !reflect.DeepEqual(got, []any{"3"}) {
		t.Fatalf("search+filter = %v", got)
	}
	g.RemoveAll()
	ops := []struct {
		f    ColumnFilter
		want []any
	}{
		{ColumnFilter{Col: 1, Op: OpContains, Value: "A"}, []any{"2", "3", "10"}},
		{ColumnFilter{Col: 1, Op: OpEquals, Value: "Bob"}, []any{"1"}},
		{ColumnFilter{Col: 1, Op: OpNotEquals, Value: "Bob"}, []any{"2", "3", "10"}},
		{ColumnFilter{Col: 0, Op: OpLess, Value: "3"}, []any{"1", "2"}},
		{ColumnFilter{Col: 0, Op: OpLessEq, Value: "3"}, []any{"1", "2", "3"}},
		{ColumnFilter{Col: 0, Op: OpGreater, Value: "3"}, []any{"10"}}, // numeric, not "10" < "3"
		{ColumnFilter{Col: 0, Op: OpGreaterEq, Value: "3"}, []any{"3", "10"}},
		{ColumnFilter{Col: 2, Op: OpIsNull}, []any{"10"}},
		{ColumnFilter{Col: 2, Op: OpNotNull}, []any{"1", "2", "3"}},
		{ColumnFilter{Col: 2, Op: OpNotEquals, Value: "Paris"}, []any{"1", "3"}}, // NULL never matches
	}
	for _, o := range ops {
		g.AddFilter(o.f)
		if got := column(g, 0); !reflect.DeepEqual(got, o.want) {
			t.Errorf("%v %q: %v want %v", o.f.Op, o.f.Value, got, o.want)
		}
		g.Action("clear_filters") // one active → removed directly
		if g.State().Active() || g.View() != nil {
			t.Fatal("clear_filters left state")
		}
	}
}

func TestLocalViewAbsorbsLazyRowsAndLoadAll(t *testing.T) {
	f := newFake(10, 30)
	g := NewWithSource(f, Options{FetchSize: 10})
	s := newScreen(t, 40, 30) // page larger than the rows: Draw fetches
	g.Action("sort")
	g.Action("sort") // id desc
	if st := g.Status(); !strings.Contains(st, "sorted within 10 loaded rows") {
		t.Fatalf("status %q", st)
	}
	draw(s, g) // fetches the next chunk (near end)
	draw(s, g)
	if f.Len() < 20 {
		t.Fatalf("no lazy fetch: %d", f.Len())
	}
	g.Status() // brings the view up to date like the next Draw
	if got := g.content.row(0)[0]; got != strconv.Itoa(f.Len()) {
		t.Fatalf("lazy rows not sorted in: first = %v", got)
	}
	g.Action("load_all")
	if f.Len() != 30 {
		t.Fatalf("load_all: %d", f.Len())
	}
	if st := g.Status(); !strings.Contains(st, "sorted within 30 loaded rows") {
		t.Fatalf("status %q", st)
	}
	if got := g.content.row(0)[0]; got != "30" {
		t.Fatalf("first after load_all = %v", got)
	}
	// A filter extends incrementally.
	f2 := newFake(10, 100)
	g2 := NewWithSource(f2, Options{FetchSize: 10})
	g2.SetSearch("row1")
	if got := column(g2, 0); !reflect.DeepEqual(got, []any{"1", "10"}) {
		t.Fatalf("filter = %v", got)
	}
	f2.Fetch(10)
	g2.refreshView()
	if n := g2.content.rows(); n != 11 { // 1, 10..19
		t.Fatalf("incremental rows = %d", n)
	}
}

func TestViewSemantics(t *testing.T) {
	cols := []exec.ColumnInfo{{Name: "n", Numeric: true}, {Name: "s"}}
	rows := []exec.Row{{"2", "Apple"}, {"10", "banana"}, {nil, "apricot"}, {"1", "cherry"}}
	g := New(exec.NewResult("t", cols, rows), Options{})
	if g.View() != nil {
		t.Fatal("no view expected")
	}
	g.SetSearch("ap")
	v := g.View()
	if v == nil || v.Sorted() {
		t.Fatalf("filter-only view: %v", v)
	}
	if !v.Keep(rows[0]) || v.Keep(rows[1]) || !v.Keep(rows[2]) {
		t.Fatal("Keep")
	}
	g.Action("sort") // n asc
	v2 := g.View()
	if !v2.Sorted() {
		t.Fatal("Sorted")
	}
	got := v2.Order(rows)
	if len(got) != 2 || got[0][0] != nil || got[1][0] != "2" {
		t.Fatalf("Order = %v", got)
	}
	if rows[0][0] != "2" {
		t.Fatal("Order modified its input")
	}
	// The returned view is a snapshot.
	g.RemoveAll()
	if !v2.Sorted() || g.View() != nil {
		t.Fatal("view not immutable / not cleared")
	}
	// Exported interface lookup as the app does it.
	if _, ok := any(g).(interface{ View() exec.View }); !ok {
		t.Fatal("View signature")
	}
}

func TestPivotRendering(t *testing.T) {
	g := New(exec.NewResult("t", custCols, custRows()), Options{PivotColumnWidth: 12})
	s := newScreen(t, 60, 8)
	if !g.Action("pivot") || !g.Pivot() {
		t.Fatal("pivot")
	}
	draw(s, g)
	txt := screenText(s)
	if hdr := line(s, 0); !strings.Contains(hdr, "1") || !strings.Contains(hdr, "2") {
		t.Fatalf("record header %q", hdr)
	}
	for i, name := range []string{"id", "name", "city"} {
		if l := line(s, i+1); !strings.HasPrefix(l, name) {
			t.Fatalf("attribute row %d = %q\n%s", i, l, txt)
		}
	}
	if !strings.Contains(line(s, 2), "Bob") || !strings.Contains(line(s, 2), "alice") {
		t.Fatalf("name row %q", line(s, 2))
	}
	// Attribute names are bold and not selectable; selection starts on (id, record 1).
	if r, c := g.CurrentCell(); r != 0 || c != 0 {
		t.Fatalf("cursor %d,%d", r, c)
	}
	// Record columns have the pivot width: "Bob" then the next record at 4+1+12+1.
	l := line(s, 2)
	if i := strings.Index(l, "alice"); uniseg.StringWidth(l[:i]) != 4+1+12+1 {
		t.Fatalf("record column width: alice at %d in %q", i, l)
	}
	// NULL is dim in the pivot too.
	x, y, ok := findCell(s, "NULL")
	if !ok {
		t.Fatalf("no NULL:\n%s", txt)
	}
	if _, st, _ := s.Get(x, y); attrs(st)&tcell.AttrDim == 0 {
		t.Error("NULL not dim")
	}
	// Scrolling right keeps the attribute column fixed.
	key(g, tcell.KeyRight)
	key(g, tcell.KeyRight)
	key(g, tcell.KeyRight)
	draw(s, g)
	if r, _ := g.CurrentCell(); r != 3 {
		t.Fatalf("record after Right = %d", r)
	}
	for i, name := range []string{"id", "name", "city"} {
		if l := line(s, i+1); !strings.HasPrefix(l, name) {
			t.Fatalf("attribute column scrolled away: %q", l)
		}
	}
	if !strings.Contains(line(s, 2), "dave") {
		t.Fatalf("record 4 not visible: %q", line(s, 2))
	}
	// Left from the first record stays on records.
	key(g, tcell.KeyHome)
	key(g, tcell.KeyLeft)
	key(g, tcell.KeyUp)
	key(g, tcell.KeyPgUp)
	if r, c := g.CurrentCell(); r != 0 || c != 0 {
		t.Fatalf("Home/Left/Up: %d,%d", r, c)
	}
	// Sort markers appear on the attribute names; sort refers to the attribute row.
	key(g, tcell.KeyDown) // name
	g.Action("sort")
	draw(s, g)
	if l := line(s, 2); !strings.HasPrefix(l, "name ▲1") || !strings.Contains(l, "Bob") {
		t.Fatalf("pivot sort: %q", l)
	}
	if r, c := g.CurrentCell(); c != 1 || r != 0 {
		t.Fatalf("cursor after sort %d,%d", r, c)
	}
	// Enter reports the record and attribute.
	var gr, gc int
	var gv any
	g.opts.OnSelect = func(r, c int, v any) { gr, gc, gv = r, c, v }
	key(g, tcell.KeyRight)
	key(g, tcell.KeyEnter)
	if gr != 2 || gc != 1 || gv != "Carol" {
		t.Fatalf("OnSelect(%d,%d,%v)", gr, gc, gv)
	}
}

func TestPivotPrimaryKeyHeader(t *testing.T) {
	rows := []exec.Row{{"101", "Bob", "Berlin"}, {"205", "alice", "Paris"}}
	g := New(originResult(rows), Options{PivotColumnWidth: 10})
	s := newScreen(t, 60, 6)
	g.Action("pivot")
	draw(s, g)
	hdr := line(s, 0)
	if !strings.Contains(hdr, "101") || !strings.Contains(hdr, "205") {
		t.Fatalf("pk header %q", hdr)
	}
}

func TestPivotLazyLoadHorizontal(t *testing.T) {
	f := newFake(20, 1000)
	g := NewWithSource(f, Options{FetchSize: 50, PivotColumnWidth: 8})
	s := newScreen(t, 60, 6) // about 5 record columns visible
	g.Action("pivot")
	draw(s, g)
	if f.fetches != 0 {
		t.Fatalf("fetched before moving: %d", f.fetches)
	}
	// Vertical moves go through attributes and never fetch.
	key(g, tcell.KeyDown)
	key(g, tcell.KeyDown)
	if f.fetches != 0 {
		t.Fatal("vertical move fetched")
	}
	if _, c := g.CurrentCell(); c != 1 {
		t.Fatalf("attribute = %d", c)
	}
	for i := 0; i < 12; i++ {
		key(g, tcell.KeyRight)
	}
	if f.fetches != 0 {
		t.Fatalf("fetched too early (record 12 of 20): %d", f.fetches)
	}
	for i := 0; i < 4; i++ {
		key(g, tcell.KeyRight)
	}
	if f.fetches != 1 || f.Len() != 70 {
		t.Fatalf("near end: fetches=%d len=%d", f.fetches, f.Len())
	}
	key(g, tcell.KeyEnd)
	if r, _ := g.CurrentCell(); r != 69 {
		t.Fatalf("End record = %d", r)
	}
	if f.Len() != 120 {
		t.Fatalf("End did not fetch: %d", f.Len())
	}
	draw(s, g)
	if !strings.Contains(screenText(s), "row70") {
		t.Fatalf("record 70 not visible:\n%s", screenText(s))
	}
	if !strings.HasPrefix(line(s, 1), "id") {
		t.Fatalf("attribute column lost: %q", line(s, 1))
	}
}

func TestPivotCursorPreserved(t *testing.T) {
	f := newFake(100, 100)
	f.cols = append(f.cols, exec.ColumnInfo{Name: "c3"}, exec.ColumnInfo{Name: "c4"})
	f.gen = func(i int) exec.Row {
		return exec.Row{strconv.Itoa(i + 1), "row" + strconv.Itoa(i+1), "x" + strconv.Itoa(i+1), "y" + strconv.Itoa(i+1)}
	}
	f.rows = nil
	f.Fetch(100)
	g := NewWithSource(f, Options{})
	s := newScreen(t, 80, 12)
	draw(s, g)
	for i := 0; i < 37; i++ {
		key(g, tcell.KeyDown)
	}
	key(g, tcell.KeyRight)
	key(g, tcell.KeyRight)
	draw(s, g)
	if r, c := g.CurrentCell(); r != 37 || c != 2 {
		t.Fatalf("start %d,%d", r, c)
	}
	g.Action("pivot")
	draw(s, g)
	if r, c := g.CurrentCell(); r != 37 || c != 2 {
		t.Fatalf("pivot %d,%d", r, c)
	}
	if v, _ := g.CurrentValue(); v != "x38" {
		t.Fatalf("pivot value %v", v)
	}
	if !strings.Contains(screenText(s), "x38") {
		t.Fatalf("record not visible in pivot:\n%s", screenText(s))
	}
	key(g, tcell.KeyRight) // next record
	key(g, tcell.KeyDown)  // next attribute
	draw(s, g)
	if r, c := g.CurrentCell(); r != 38 || c != 3 {
		t.Fatalf("moved in pivot %d,%d", r, c)
	}
	g.Action("pivot")
	draw(s, g)
	if r, c := g.CurrentCell(); r != 38 || c != 3 {
		t.Fatalf("back %d,%d", r, c)
	}
	if !strings.Contains(screenText(s), "y39") {
		t.Fatalf("record not visible after toggling back:\n%s", screenText(s))
	}
	// With a local sort the cursor still refers to the same record.
	g.Action("sort")
	g.Action("sort") // c4 desc (string: y99 … y1)
	for i := 0; i < 5; i++ {
		key(g, tcell.KeyDown)
	}
	rec, _ := g.CurrentRecord()
	g.Action("pivot")
	if r2, _ := g.CurrentRecord(); r2 != rec {
		t.Fatalf("sorted pivot record %d != %d", r2, rec)
	}
	g.Action("pivot")
	if r2, _ := g.CurrentRecord(); r2 != rec {
		t.Fatalf("sorted back record %d != %d", r2, rec)
	}
}

type fakeModal struct {
	p      tview.Primitive
	title  string
	closed func()
	opens  int
	closes int
}

func (m *fakeModal) options(o Options) Options {
	o.OpenPrompt = func(p tview.Primitive, title string, w, h int, closed func()) {
		m.p, m.title, m.closed = p, title, closed
		m.opens++
		p.Focus(func(q tview.Primitive) { q.Focus(func(tview.Primitive) {}) })
	}
	o.ClosePrompt = func() { m.closes++; m.p = nil }
	return o
}

func TestPrompts(t *testing.T) {
	m := &fakeModal{}
	g := New(exec.NewResult("t", custCols, custRows()), m.options(Options{}))
	// Quick search.
	if !g.Action("search") || m.p == nil || !g.PromptOpen() {
		t.Fatal("search prompt not opened")
	}
	typeText(m.p, "par")
	keyP(m.p, tcell.KeyEnter)
	if m.closes != 1 || g.PromptOpen() || g.State().Search != "par" {
		t.Fatalf("search: closes=%d state=%+v", m.closes, g.State())
	}
	if got := column(g, 0); !reflect.DeepEqual(got, []any{"2"}) {
		t.Fatalf("search rows %v", got)
	}
	// Esc cancels without change.
	g.Action("search")
	typeText(m.p, "zzz")
	keyP(m.p, tcell.KeyEscape)
	if g.State().Search != "par" || m.closes != 2 {
		t.Fatal("Esc changed the search")
	}
	g.RemoveAll()

	// Column filter: operator list, then value.
	key(g, tcell.KeyRight)
	key(g, tcell.KeyRight) // city
	if !g.Action("filter_column") || m.title != "Filter: city" {
		t.Fatalf("filter prompt %q", m.title)
	}
	runeKey(m.p, '2') // starts with
	typeText(m.p, "be")
	keyP(m.p, tcell.KeyEnter)
	if st := g.State(); len(st.Filters) != 1 || st.Filters[0] != (ColumnFilter{Col: 2, Op: OpStartsWith, Value: "be"}) {
		t.Fatalf("filter state %+v", st.Filters)
	}
	if got := column(g, 0); !reflect.DeepEqual(got, []any{"1", "3"}) {
		t.Fatalf("filter rows %v", got)
	}
	// is NULL applies without a value.
	g.RemoveAll()
	g.Action("filter_column")
	runeKey(m.p, '9')
	if got := column(g, 0); !reflect.DeepEqual(got, []any{"10"}) || m.p != nil {
		t.Fatalf("is NULL rows %v", got)
	}
	// Equals is pre-filled with the current cell value.
	g.RemoveAll()
	key(g, tcell.KeyLeft) // name, record 0 = Bob
	g.Action("filter_column")
	runeKey(m.p, '3')
	keyP(m.p, tcell.KeyEnter)
	if st := g.State(); len(st.Filters) != 1 || st.Filters[0].Value != "Bob" || st.Filters[0].Op != OpEquals {
		t.Fatalf("prefill %+v", st.Filters)
	}
	// Draw the staged prompt to be sure it renders.
	s := newScreen(t, 40, 14)
	g.Action("filter_column")
	draw(s, m.p)
	if !strings.Contains(screenText(s), "name contains") || !strings.Contains(screenText(s), "name is not NULL") {
		t.Fatalf("operator list:\n%s", screenText(s))
	}
	keyP(m.p, tcell.KeyEscape)

	// x with several active: menu.
	g.SetSearch("o")
	if items := g.ActiveItems(); !reflect.DeepEqual(items, []string{`search: "o"`, `filter: name = "Bob"`}) {
		t.Fatalf("items %q", items)
	}
	if !g.Action("clear_filters") || m.title != "Remove" {
		t.Fatal("no remove menu")
	}
	runeKey(m.p, '2')
	if st := g.State(); st.Search != "o" || len(st.Filters) != 0 {
		t.Fatalf("after menu remove: %+v", st)
	}
	g.AddFilter(ColumnFilter{Col: 0, Op: OpNotNull})
	g.Action("clear_filters")
	runeKey(m.p, 'a')
	if g.State().Active() {
		t.Fatal("all not removed")
	}
	// The app may close the modal itself.
	g.Action("search")
	m.closed()
	if g.PromptOpen() {
		t.Fatal("closed callback ignored")
	}
	// WHERE prompt is table-origin only and pre-filled; completion hook.
	log := &requeryLog{res: func() *exec.Result { return exec.NewResult("q", custCols, custRows()) }}
	var completed string
	o := m.options(Options{Dialect: &mysql.MySQL{}, Requery: log.fn, CompleteWhere: func(s string) []string { completed = s; return nil }})
	tg := New(originResult(custRows()), o)
	tg.Action("filter_where")
	typeText(m.p, "id = 3")
	keyP(m.p, tcell.KeyEnter)
	if len(log.sqls) != 1 || log.sqls[0] != "SELECT * FROM `shop`.`customers` WHERE (id = 3)" {
		t.Fatalf("where sql %q", log.sqls)
	}
	if completed == "" {
		t.Error("CompleteWhere not consulted")
	}
	tg.Action("filter_where")
	if in, ok := m.p.(*tview.InputField); !ok || in.GetText() != "id = 3" {
		t.Fatal("where not pre-filled")
	}
	// No OpenPrompt: the prompt actions are not handled.
	n := New(exec.NewResult("t", custCols, custRows()), Options{})
	if n.Action("search") || n.Action("filter_column") || n.Action("unknown") {
		t.Fatal("handled without OpenPrompt")
	}
}
