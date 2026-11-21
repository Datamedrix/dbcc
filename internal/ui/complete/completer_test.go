package complete

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"dbcc/internal/dialect"
	"dbcc/internal/dialect/mysql"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
)

// fakeLoader serves a fixed catalog: databases → (schema →) objects and
// tables with columns.
type fakeLoader struct {
	mu      sync.Mutex
	dbs     []string
	schemas map[string][]string      // db -> schemas (schema dialects)
	objs    map[string][]meta.Object // "db" or "db.schema"
	tables  map[string][]meta.Column // "db.table" or "db.schema.table"
	loads   map[string]int
}

func (f *fakeLoader) hit(k string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loads == nil {
		f.loads = map[string]int{}
	}
	f.loads[k]++
}

func (f *fakeLoader) count(k string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.loads[k] }

func (f *fakeLoader) Databases(ctx context.Context) ([]meta.Object, error) {
	f.hit("dbs")
	var out []meta.Object
	for _, d := range f.dbs {
		out = append(out, meta.Object{Name: d, Kind: meta.KindDatabase})
	}
	return out, nil
}

func (f *fakeLoader) Schemas(ctx context.Context, db string) ([]meta.Object, error) {
	f.hit("schemas:" + db)
	var out []meta.Object
	for _, s := range f.schemas[db] {
		out = append(out, meta.Object{Name: s, Kind: meta.KindSchema})
	}
	return out, nil
}

func key(p meta.Path) string {
	k := p.Database
	if p.Schema != "" {
		k += "." + p.Schema
	}
	return k
}

func (f *fakeLoader) Objects(ctx context.Context, at meta.Path) ([]meta.Object, error) {
	f.hit("objs:" + key(at))
	return f.objs[key(at)], nil
}

func (f *fakeLoader) Table(ctx context.Context, obj meta.Path) (*meta.Table, error) {
	k := key(obj) + "." + obj.Object
	f.hit("table:" + k)
	cols, ok := f.tables[k]
	if !ok {
		return nil, fmt.Errorf("%w: %s", meta.ErrNotFound, k)
	}
	return &meta.Table{Path: obj, Kind: meta.KindTable, Columns: cols}, nil
}

func cols(spec string) []meta.Column {
	var out []meta.Column
	for i, f := range strings.Fields(spec) {
		name, typ, _ := strings.Cut(f, ":")
		out = append(out, meta.Column{Name: name, Position: i + 1, DataType: typ})
	}
	return out
}

func shopLoader() *fakeLoader {
	return &fakeLoader{
		dbs: []string{"hr", "shop"},
		objs: map[string][]meta.Object{
			"shop": {
				{Name: "Customers", Kind: meta.KindTable},
				{Name: "orders", Kind: meta.KindTable},
				{Name: "order items", Kind: meta.KindTable},
				{Name: "select", Kind: meta.KindTable},
				{Name: "v_open", Kind: meta.KindView},
				{Name: "do_thing", Kind: meta.KindProcedure},
				{Name: "f_total", Kind: meta.KindFunction},
			},
			"hr": {{Name: "staff", Kind: meta.KindTable}, {Name: "hire", Kind: meta.KindProcedure}},
		},
		tables: map[string][]meta.Column{
			"shop.Customers":   cols("id:int name:varchar(40) email:varchar(80) order:int"),
			"shop.orders":      cols("id:int customer_id:int total:decimal(10,2) created:datetime"),
			"shop.order items": cols("order_id:int qty:int"),
			"shop.v_open":      cols("id:int total:decimal(10,2)"),
			"hr.staff":         cols("id:int first_name:varchar(20)"),
		},
	}
}

func newCache(l meta.Loader) *meta.Cache {
	return meta.NewCache(func(conn string) (meta.Loader, bool) {
		if conn == "dev" {
			return l, true
		}
		return nil, false
	})
}

func mysqlCompleter(l meta.Loader) *Completer {
	return New(newCache(l), &mysql.MySQL{}, meta.Path{Conn: "dev", Database: "shop"})
}

// cursorAt splits "text|more" into text without the marker and the offset.
func cursorAt(s string) (string, int) {
	i := strings.Index(s, "|")
	return s[:i] + s[i+1:], i
}

func texts(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Text
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCandidatesMySQL(t *testing.T) {
	cases := []struct {
		src  string
		want []string // exact list, or with prefix "~" only containment of these
	}{
		{"SELECT * FROM |", []string{"Customers", "orders", "`order items`", "`select`", "v_open", "hr", "shop"}},
		{"SELECT * FROM or|", []string{"orders", "`order items`"}},
		{"SELECT * FROM `or|", []string{"`orders`", "`order items`"}},
		{"SELECT * FROM cu|", []string{"Customers"}},
		{"SELECT * FROM hr.|", []string{"staff"}},
		{"SELECT * FROM HR.s|", []string{"staff"}},
		{"CALL |", []string{"do_thing"}},
		{"CALL hr.|", []string{"hire"}},
		{"SELECT c.| FROM customers c JOIN orders o ON o.customer_id = c.id",
			[]string{"id", "name", "email", "`order`"}},
		{"SELECT o.| FROM customers c JOIN orders o ON o.customer_id = c.id",
			[]string{"id", "customer_id", "total", "created"}},
		{"SELECT * FROM shop.customers AS c WHERE c.|", []string{"id", "name", "email", "`order`"}},
		{"SELECT * FROM shop.customers AS c WHERE c.na|", []string{"name"}},
		{"SELECT s.| FROM hr.staff s", []string{"id", "first_name"}},
		{"SELECT x.| FROM customers c", nil},
		{"SELECT hr.| FROM customers c", []string{"staff", "hire"}}, // "hr." is no table: database fallback
		{"SELECT oi.| FROM `order items` oi", []string{"order_id", "qty"}},
		{"SELECT d.| FROM (SELECT 1) d", nil},
		{"SELECT 'cu|", nil},
		{"SELECT * FROM t -- c|", nil},
	}
	for _, c := range cases {
		comp := mysqlCompleter(shopLoader())
		src, cur := cursorAt(c.src)
		got := texts(comp.Candidates(context.Background(), src, cur))
		if !eq(got, c.want) {
			t.Errorf("%s\n got  %q\n want %q", c.src, got, c.want)
		}
	}
}

func TestCandidatesAnyContext(t *testing.T) {
	comp := mysqlCompleter(shopLoader())
	src, cur := cursorAt("SELECT | FROM customers c JOIN orders o ON 1")
	items := comp.Candidates(context.Background(), src, cur)
	// Columns of both tables first (deduplicated by name), then keywords
	// and functions.
	if len(items) < 8 {
		t.Fatalf("too few items: %v", items)
	}
	wantCols := []string{"id", "name", "email", "`order`", "customer_id", "total", "created"}
	if got := texts(items[:len(wantCols)]); !eq(got, wantCols) {
		t.Fatalf("columns first: %q", got)
	}
	for _, it := range items[:len(wantCols)] {
		if it.Kind != KindColumn {
			t.Errorf("%s kind %s", it.Text, it.Kind)
		}
	}
	if items[0].Detail != "int  customers" {
		t.Errorf("detail %q", items[0].Detail)
	}
	sawKW, sawFn := false, false
	for _, it := range items[len(wantCols):] {
		switch it.Kind {
		case KindKeyword:
			sawKW = true
			if sawFn {
				t.Fatal("keywords must come before functions")
			}
		case KindFunction:
			sawFn = true
		case KindColumn:
			t.Fatalf("column after keywords: %v", it)
		}
	}
	if !sawKW || !sawFn {
		t.Fatal("keywords or functions missing")
	}

	// Lower-case prefix → lower-case keywords; columns matching come first.
	src, cur = cursorAt("SELECT c| FROM customers c")
	items = comp.Candidates(context.Background(), src, cur)
	foundCase := false
	for _, it := range items {
		if it.Kind == KindKeyword && it.Text == "case" {
			foundCase = true
		}
		if it.Kind == KindKeyword && it.Text == "CASE" {
			t.Fatal("keyword not in prefix case")
		}
	}
	if !foundCase {
		t.Fatal("keyword case missing")
	}
	// Upper-case prefix → upper-case keywords.
	src, cur = cursorAt("SELECT * FROM customers WH|")
	if got := texts(comp.Candidates(context.Background(), src, cur)); !containsAll(got, "WHERE") {
		t.Fatalf("upper prefix: %q", got)
	}
}

func containsAll(have []string, want ...string) bool {
	m := map[string]bool{}
	for _, h := range have {
		m[h] = true
	}
	for _, w := range want {
		if !m[w] {
			return false
		}
	}
	return true
}

func TestCompleteResultRange(t *testing.T) {
	comp := mysqlCompleter(shopLoader())
	src, cur := cursorAt("SELECT * FROM `or|")
	res := comp.Complete(context.Background(), src, cur)
	if res.Start != strings.Index(src, "`") || res.Prefix != "or" {
		t.Fatalf("range %+v", res)
	}
	src, cur = cursorAt("SELECT c.na| FROM customers c")
	res = comp.Complete(context.Background(), src, cur)
	if res.Start != cur-2 || res.Prefix != "na" {
		t.Fatalf("range %+v", res)
	}
	// Whole buffer: only the statement at the cursor counts.
	src, cur = cursorAt("SELECT * FROM orders o;\nSELECT o.| FROM customers o;\nSELECT 1")
	if got := texts(comp.Candidates(context.Background(), src, cur)); !eq(got, []string{"id", "name", "email", "`order`"}) {
		t.Fatalf("multi-statement buffer: %q", got)
	}
}

func TestCandidatesCachedLazily(t *testing.T) {
	l := shopLoader()
	comp := mysqlCompleter(l)
	if l.count("objs:shop") != 0 {
		t.Fatal("loaded before use")
	}
	src, cur := cursorAt("SELECT c.| FROM customers c")
	for i := 0; i < 3; i++ {
		comp.Candidates(context.Background(), src, cur)
	}
	if l.count("objs:shop") != 1 || l.count("table:shop.Customers") != 1 {
		t.Fatalf("loads: %v", l.loads)
	}
	// DDL invalidates.
	if !InvalidateOnDDL(comp.Cache, "dev", comp.Dialect, exec.Message{Stmt: "ALTER TABLE customers ADD x INT"}) {
		t.Fatal("ALTER not treated as DDL")
	}
	if InvalidateOnDDL(comp.Cache, "dev", comp.Dialect, exec.Message{Stmt: "SELECT 1"}) {
		t.Fatal("SELECT treated as DDL")
	}
	comp.Candidates(context.Background(), src, cur)
	if l.count("table:shop.Customers") != 2 {
		t.Fatalf("not reloaded after DDL: %v", l.loads)
	}
}

func TestCandidatesNotConnected(t *testing.T) {
	comp := New(newCache(shopLoader()), &mysql.MySQL{}, meta.Path{Conn: "other", Database: "x"})
	src, cur := cursorAt("SELECT * FROM |")
	if got := comp.Candidates(context.Background(), src, cur); len(got) != 0 {
		t.Fatalf("no loader should give nothing: %v", got)
	}
	src, cur = cursorAt("SEL|")
	if got := texts(comp.Candidates(context.Background(), src, cur)); !containsAll(got, "SELECT") {
		t.Fatalf("keywords without metadata: %q", got)
	}
	if got := (&Completer{}).Candidates(context.Background(), "SELECT ", 7); got != nil {
		t.Fatalf("no dialect: %v", got)
	}
}

// schemaDialect is MySQL with a schema level and ANSI quoting, to test the
// PostgreSQL-style resolution without depending on the postgres package.
type schemaDialect struct{ *mysql.MySQL }

func (schemaDialect) HasSchemas() bool { return true }
func (schemaDialect) QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

var _ dialect.Dialect = schemaDialect{}

func TestCandidatesSchemas(t *testing.T) {
	l := &fakeLoader{
		dbs:     []string{"app", "other"},
		schemas: map[string][]string{"app": {"public", "sales"}, "other": {"public", "x"}},
		objs: map[string][]meta.Object{
			"app.public":   {{Name: "customers", Kind: meta.KindTable}, {Name: "Mixed", Kind: meta.KindTable}, {Name: "p1", Kind: meta.KindProcedure}},
			"app.sales":    {{Name: "orders", Kind: meta.KindTable}},
			"other.public": {{Name: "remote", Kind: meta.KindTable}},
		},
		tables: map[string][]meta.Column{
			"app.public.customers": cols("id:integer name:text"),
			"app.sales.orders":     cols("id:integer amount:numeric"),
			"other.public.remote":  cols("rid:integer"),
		},
	}
	comp := New(newCache(l), schemaDialect{&mysql.MySQL{}}, meta.Path{Conn: "dev", Database: "app", Schema: "public"})
	cases := []struct {
		src  string
		want []string
	}{
		{"SELECT * FROM |", []string{"customers", `"Mixed"`, "public", "sales", "app", "other"}},
		{"SELECT * FROM sales.|", []string{"orders"}},
		{"SELECT * FROM other.|", []string{"public", "x"}}, // not a schema: schemas of database "other"
		{"SELECT * FROM other.public.|", []string{"remote"}},
		{"SELECT o.| FROM sales.orders o", []string{"id", "amount"}},
		{"SELECT c.| FROM customers c", []string{"id", "name"}},
		{"SELECT r.| FROM other.public.remote r", []string{"rid"}},
		{"CALL |", []string{"p1"}},
	}
	for _, c := range cases {
		src, cur := cursorAt(c.src)
		got := texts(comp.Candidates(context.Background(), src, cur))
		if !eq(got, c.want) {
			t.Errorf("%s\n got  %q\n want %q", c.src, got, c.want)
		}
	}
}

func TestNeedsQuote(t *testing.T) {
	my := &mysql.MySQL{}
	ansi := schemaDialect{my}
	cases := []struct {
		name     string
		my, ansi bool
	}{
		{"customers", false, false},
		{"Customers", false, true},
		{"_x1$", false, false},
		{"1abc", true, true},
		{"order items", true, true},
		{"select", true, true},
		{"ORDER", true, true},
		{"name", false, false},
		{"status", false, false},
		{"größe", true, true},
		{"", true, true},
		{"a-b", true, true},
	}
	for _, c := range cases {
		if got := NeedsQuote(my, c.name); got != c.my {
			t.Errorf("mysql %q: %v", c.name, got)
		}
		if got := NeedsQuote(ansi, c.name); got != c.ansi {
			t.Errorf("ansi %q: %v", c.name, got)
		}
	}
	if QuoteIfNeeded(my, "order") != "`order`" || QuoteIfNeeded(my, "id") != "id" || QuoteIfNeeded(ansi, "Mixed") != `"Mixed"` {
		t.Fatal("QuoteIfNeeded")
	}
	if QuoteIfNeeded(nil, "a b") != "a b" {
		t.Fatal("nil dialect")
	}
	if NeedsQuote(overrideDialect{my}, "anything") != true || NeedsQuote(overrideDialect{my}, "ok") {
		t.Fatal("NeedsQuote override")
	}
}

type overrideDialect struct{ *mysql.MySQL }

func (overrideDialect) NeedsQuote(name string) bool { return name != "ok" }

func TestIsDDL(t *testing.T) {
	tok := (&mysql.MySQL{}).Tokenizer()
	for s, want := range map[string]bool{
		"CREATE TABLE x (id int)": true, "  /* c */ drop view v": true, "ALTER TABLE t ADD c INT": true,
		"RENAME TABLE a TO b": true, "TRUNCATE t": true, "SELECT 1": false, "INSERT INTO t VALUES (1)": false,
		"": false, "CALL p()": false,
	} {
		if IsDDL(tok, s) != want {
			t.Errorf("IsDDL(%q) != %v", s, want)
		}
	}
}
