//go:build integration

package postgres

// Integration tests against a real PostgreSQL server:
//
//	DBCC_IT_POSTGRES=127.0.0.1:15432 DBCC_IT_PG_PW=<postgres password>
//	go test -tags integration -count=1 ./internal/dialect/postgres/ [-update]
//
// -update rewrites testdata/golden/<table>.json.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/session"
	"dbcc/internal/sqltext"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	itDB    = "dbcc_it_pg"
	otherDB = "dbcc_it_pg_other"
)

type env struct {
	ctx  context.Context
	d    *Postgres
	db   *sql.DB // pool on itDB
	cred config.Credentials
	prof *config.Profile
}

func itCred(t *testing.T) (config.Credentials, *config.Profile) {
	t.Helper()
	addr := os.Getenv("DBCC_IT_POSTGRES")
	if addr == "" {
		t.Skip("DBCC_IT_POSTGRES not set")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	pn, _ := strconv.Atoi(port)
	cred := config.Credentials{Host: host, Port: pn, User: "postgres", Password: os.Getenv("DBCC_IT_PG_PW")}
	prof := &config.Profile{Name: "pg", Driver: "postgres", Host: host, Port: pn, User: "postgres",
		Database: itDB, Password: "env:DBCC_IT_PG_PW", Params: map[string]string{"sslmode": "disable"}}
	return cred, prof
}

func (e *env) open(t *testing.T, db string) *sql.DB {
	t.Helper()
	d, err := e.d.Open(e.ctx, e.prof, e.cred, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.db.ExecContext(e.ctx, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func runScript(ctx context.Context, c dialect.Conn, src string) error {
	stmts, _ := sqltext.NewPostgres().Split(src)
	for _, st := range stmts {
		if _, err := c.ExecContext(ctx, st.Text); err != nil {
			return fmt.Errorf("line %d: %w\n%s", st.Line, err, st.Text)
		}
	}
	return nil
}

var setupMu sync.Mutex

// setup recreates itDB (with testdata/schema.sql) and otherDB.
func setup(t *testing.T) *env {
	t.Helper()
	setupMu.Lock()
	defer setupMu.Unlock()
	cred, prof := itCred(t)
	e := &env{ctx: context.Background(), d: &Postgres{}, cred: cred, prof: prof}
	admin := e.open(t, "postgres")
	for _, db := range []string{itDB, otherDB} {
		for _, q := range []string{"DROP DATABASE IF EXISTS " + db + " WITH (FORCE)", "CREATE DATABASE " + db} {
			if _, err := admin.ExecContext(e.ctx, q); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.db = e.open(t, itDB)
	dd, err := e.d.Detect(e.ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	e.d = dd.(*Postgres)
	src, err := os.ReadFile(filepath.Join("testdata", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runScript(e.ctx, e.db, string(src)); err != nil {
		t.Fatal(err)
	}
	other := e.open(t, otherDB)
	if err := runScript(e.ctx, other, "CREATE SCHEMA otherschema; CREATE TABLE otherschema.t (id int primary key);"); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) seed(t *testing.T) {
	t.Helper()
	e.exec(t, `INSERT INTO app.parent (a, b, label) VALUES (1, 'a', 'p1'), (2, 'b', NULL)`)
	e.exec(t, `INSERT INTO app.items (code, name, price, qty, parent_a, parent_b, tags, doc, bin, status) VALUES
		('c1', 'Alpha Beta', 12.50, 2, 1, 'a', '{x,y}', '{"k": "Needle"}', '\x0102', 'active'),
		('c2', 'gamma', 3, 1, NULL, NULL, NULL, NULL, NULL, 'new'),
		('c3', 'DELTA needle', 0, 5, 2, 'b', NULL, NULL, NULL, 'it''s done')`)
	e.exec(t, `INSERT INTO app.logs (id, created, msg) VALUES (1, '2023-05-01', 'a'), (2, '2024-05-01', 'b'), (3, '2030-01-01', 'c')`)
}

func app(obj string) meta.Path { return meta.Path{Database: itDB, Schema: "app", Object: obj} }

func (e *env) describe(t *testing.T, obj string) *meta.Table {
	t.Helper()
	tbl, err := e.d.DescribeTable(e.ctx, e.db, app(obj))
	if err != nil {
		t.Fatalf("describe %s: %v", obj, err)
	}
	return tbl
}

func masked(tbl *meta.Table) *meta.Table {
	c := *tbl
	c.Options = map[string]string{}
	for k, v := range tbl.Options {
		if k == "rows" {
			v = "<masked>"
		}
		c.Options[k] = v
	}
	return &c
}

func TestDescribeTableGolden(t *testing.T) {
	e := setup(t)
	for _, name := range []string{"parent", "items", "logs", "logs_2024", "v_items", "mv_totals"} {
		got, err := json.MarshalIndent(masked(e.describe(t, name)), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, '\n')
		file := filepath.Join("testdata", "golden", name+".json")
		if *update {
			if err := os.WriteFile(file, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%v (run with -update)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from %s:\n%s", name, file, got)
		}
	}
	if _, err := e.d.DescribeTable(e.ctx, e.db, app("no_such_table")); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing table: %v", err)
	}
	// Empty schema resolves through search_path.
	if tbl, err := e.d.DescribeTable(e.ctx, e.db, meta.Path{Object: "plain"}); err != nil || tbl.Path.Schema != "public" || tbl.Path.Database != itDB {
		t.Errorf("search_path: %v %+v", err, tbl)
	}
	// Wrong database is an error, not silently another database.
	if _, err := e.d.DescribeTable(e.ctx, e.db, meta.Path{Database: otherDB, Schema: "otherschema", Object: "t"}); err == nil {
		t.Error("describe on wrong database succeeded")
	}
}

func TestDescribeSemantics(t *testing.T) {
	e := setup(t)
	tbl := e.describe(t, "items")
	col := func(n string) *meta.Column {
		c := tbl.Column(n)
		if c == nil {
			t.Fatalf("no column %s", n)
		}
		return c
	}
	for n, want := range map[string]struct {
		v       string
		has, ex bool
	}{
		"region": {"EU", true, false}, "note": {"NULL", true, false}, "nothing": {"", false, false},
		"quote_str": {"it's", true, false}, "price": {"0.00", true, false}, "qty": {"1", true, false},
		"ratio": {"-1.5", true, false}, "status": {"new", true, false}, "code": {"", false, false},
		"created": {"now()", true, true}, "uid": {"gen_random_uuid()", true, true}, "id": {"", false, false},
		"total": {"", false, false},
	} {
		c := col(n)
		v := ""
		if c.Default != nil {
			v = *c.Default
		}
		if v != want.v || (c.Default != nil) != want.has || c.DefaultExpr != want.ex {
			t.Errorf("%s default = %q,%v,%v want %+v", n, v, c.Default != nil, c.DefaultExpr, want)
		}
	}
	if c := col("id"); !c.AutoIncrement || tbl.Options["identity.id"] != "ALWAYS" || c.DataType != "bigint" {
		t.Errorf("identity %+v %v", c, tbl.Options)
	}
	if c := col("seq_no"); !c.AutoIncrement || !strings.HasPrefix(*c.Default, "nextval(") {
		t.Errorf("serial %+v", c)
	}
	if c := col("ticket"); c.AutoIncrement {
		t.Errorf("ticket uses a shared sequence, not serial: %+v", c)
	}
	if g := col("total").Generated; g == nil || !g.Stored || !strings.Contains(g.Expression, "price") {
		t.Errorf("generated %+v", g)
	}
	if !reflect.DeepEqual(col("status").EnumValues, []string{"new", "active", "it's done"}) || col("name").Collation != "C" ||
		col("code").DataType != "character varying(20)" || col("tags").DataType != "text[]" {
		t.Errorf("types %+v %+v", col("status"), col("name"))
	}
	if fk := tbl.ForeignKeys; len(fk) != 1 || fk[0].OnDelete != "CASCADE" || fk[0].OnUpdate != "RESTRICT" ||
		!reflect.DeepEqual(fk[0].Columns, []string{"parent_a", "parent_b"}) || fk[0].RefTable.Schema != "app" {
		t.Errorf("fk %+v", fk)
	}
	if tbl.PrimaryKey == nil || tbl.PrimaryKey.Columns[0] != "id" || len(tbl.UniqueKeys) != 2 {
		t.Errorf("keys %+v %+v", tbl.PrimaryKey, tbl.UniqueKeys)
	}
	idx := map[string]meta.Index{}
	for _, ix := range tbl.Indexes {
		idx[ix.Name] = ix
	}
	if c := idx["ix_items_price_desc"].Columns; len(c) != 2 || !c[0].Descending || c[0].Name != "price NULLS LAST" {
		t.Errorf("desc %+v", c)
	}
	if c := idx["ix_items_lower_name"].Columns; len(c) != 1 || !c[0].Expression || c[0].Name != "lower(name)" {
		t.Errorf("expr %+v", c)
	}
	if ix := idx["ix_items_active"]; ix.Columns[0].Name != "code varchar_pattern_ops" || strings.Join(ix.Include, ",") != "qty" ||
		ix.Filter != "status = 'active'::app.status" && ix.Filter != "status = 'active'" {
		t.Errorf("partial %+v", ix)
	}
	if ix := idx["ix_items_doc"]; !strings.HasPrefix(ix.Type, "gin") || ix.Columns[0].Name != "doc jsonb_path_ops" {
		t.Errorf("gin %+v", ix)
	}
	if len(tbl.Triggers) != 2 || tbl.Triggers[0].Event != "INSERT OR UPDATE OF note FOR EACH ROW WHEN (new.qty > 0)" ||
		!strings.HasSuffix(tbl.Triggers[1].Event, "(disabled)") {
		t.Errorf("triggers %+v", tbl.Triggers)
	}
	if !strings.HasPrefix(e.describe(t, "logs").Options["partitioning"], "RANGE (created) 3 partitions: logs_2023, logs_2024, logs_default") {
		t.Errorf("partitioning %q", e.describe(t, "logs").Options)
	}
	if v := e.describe(t, "v_items"); v.Kind != meta.KindView || len(v.Columns) != 3 || !strings.Contains(v.ViewDefinition, "app.items") {
		t.Errorf("view %+v", v)
	}
}

func TestListing(t *testing.T) {
	e := setup(t)
	dbs, err := e.d.ListDatabases(e.ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]meta.Object{}
	for _, o := range dbs {
		seen[o.Name] = o
	}
	if _, ok := seen[itDB]; !ok || !seen["template1"].System || seen["postgres"].System {
		t.Errorf("databases %+v", dbs)
	}
	if _, ok := seen["template0"]; ok {
		t.Error("template0 listed")
	}
	if dbs[len(dbs)-1].Name != "template1" {
		t.Errorf("system databases not last: %+v", dbs)
	}
	sch, err := e.d.ListSchemas(e.ctx, e.db, itDB)
	if err != nil {
		t.Fatal(err)
	}
	ss := map[string]meta.Object{}
	for _, o := range sch {
		ss[o.Name] = o
	}
	if ss["app"].Comment != "application schema" || ss["public"].System || !ss["pg_catalog"].System || !ss["information_schema"].System {
		t.Errorf("schemas %+v", sch)
	}
	if _, err := e.d.ListSchemas(e.ctx, e.db, otherDB); err == nil {
		t.Error("ListSchemas on wrong database succeeded")
	}
	objs, err := e.d.ListObjects(e.ctx, e.db, meta.Path{Database: itDB, Schema: "app"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]meta.ObjectKind{}
	for _, o := range objs {
		if k, dup := kinds[o.Name]; dup && k == o.Kind {
			t.Errorf("duplicate %s", o.Name)
		}
		kinds[o.Name] = o.Kind
	}
	want := map[string]meta.ObjectKind{
		"items": meta.KindTable, "logs": meta.KindTable, "logs_2023": meta.KindTable, "v_items": meta.KindView,
		"mv_totals": meta.KindMaterializedView, "ticket_seq": meta.KindSequence, "items_seq_no_seq": meta.KindSequence,
		"f_double": meta.KindFunction, "f_atomic": meta.KindFunction, "trg_items_touch": meta.KindFunction,
		"p_count": meta.KindProcedure, "trg_items_bi": meta.KindTrigger,
	}
	for n, k := range want {
		if kinds[n] != k {
			t.Errorf("%s: kind %q, want %q", n, kinds[n], k)
		}
	}
	if _, ok := kinds["items_id_seq"]; ok {
		t.Error("identity sequence listed")
	}
	only, err := e.d.ListObjects(e.ctx, e.db, meta.Path{Schema: "app"}, dialect.ObjectKinds{meta.KindProcedure})
	if err != nil || len(only) != 1 || only[0].Name != "p_count" {
		t.Errorf("filtered %+v %v", only, err)
	}
}

func TestObjectSourceAndReplaceScript(t *testing.T) {
	e := setup(t)
	e.seed(t)
	for name, kind := range map[string]meta.ObjectKind{
		"v_items": meta.KindView, "mv_totals": meta.KindMaterializedView, "f_double": meta.KindFunction,
		"p_count": meta.KindProcedure, "trg_items_bi": meta.KindTrigger, "ticket_seq": meta.KindSequence, "f_atomic": meta.KindFunction,
	} {
		src, err := e.d.ObjectSource(e.ctx, e.db, app(name), kind)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.HasPrefix(src, "CREATE ") || !strings.Contains(src, name) {
			t.Errorf("%s: %s", name, src)
		}
	}
	if src, _ := e.d.ObjectSource(e.ctx, e.db, app("f_double"), meta.KindFunction); strings.Count(src, "CREATE OR REPLACE FUNCTION") != 2 {
		t.Errorf("overloads: %s", src)
	}
	if src, _ := e.d.ObjectSource(e.ctx, e.db, app("ticket_seq"), meta.KindSequence); src != `CREATE SEQUENCE "app"."ticket_seq" AS integer INCREMENT BY 5 MINVALUE 1 MAXVALUE 2147483647 START WITH 100 CACHE 1` {
		t.Errorf("sequence: %s", src)
	}
	if _, err := e.d.ObjectSource(e.ctx, e.db, app("nope"), meta.KindProcedure); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}

	// Scripts run on a session whose search_path contains neither app nor
	// public: they must not depend on it.
	other := e.open(t, itDB)
	conn, err := other.Conn(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(e.ctx, "SET search_path TO pg_catalog"); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]meta.ObjectKind{
		"v_items": meta.KindView, "mv_totals": meta.KindMaterializedView, "f_double": meta.KindFunction,
		"p_count": meta.KindProcedure, "trg_items_bi": meta.KindTrigger, "f_atomic": meta.KindFunction,
		"trg_items_touch": meta.KindFunction,
	} {
		script, err := e.d.ReplaceScript(e.ctx, e.db, app(name), kind)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if kind == meta.KindTrigger && !strings.HasPrefix(script, "CREATE OR REPLACE TRIGGER") {
			t.Errorf("trigger script: %s", script)
		}
		if err := runScript(e.ctx, conn, script); err != nil {
			t.Errorf("%s: %v\n%s", name, err, script)
		}
	}
	// The materialized view keeps its index and comment.
	mv := e.describe(t, "mv_totals")
	if len(mv.Indexes) != 1 || mv.Comment != "totals" {
		t.Errorf("mv after replace %+v", mv)
	}
	// Edit a function body through the script and run it again.
	script, _ := e.d.ReplaceScript(e.ctx, e.db, app("trg_items_touch"), meta.KindFunction)
	script = strings.Replace(script, "'touched;'", "'edited;'", 1)
	if err := runScript(e.ctx, conn, script); err != nil {
		t.Fatal(err)
	}
	e.exec(t, "INSERT INTO app.items (code, name) VALUES ('c9', 'x')")
	var note string
	if err := e.db.QueryRowContext(e.ctx, "SELECT note FROM app.items WHERE code = 'c9'").Scan(&note); err != nil || note != "NULL" {
		// note has the default 'NULL' (string), the trigger keeps it
		t.Errorf("note %q %v", note, err)
	}
	e.exec(t, "INSERT INTO app.items (code, name, note) VALUES ('c10', 'x', NULL)")
	if err := e.db.QueryRowContext(e.ctx, "SELECT note FROM app.items WHERE code = 'c10'").Scan(&note); err != nil || note != "edited;" {
		t.Errorf("edited function not active: %q %v", note, err)
	}
}

func rowsOf(t *testing.T, e *env, q string) [][]string {
	t.Helper()
	rows, err := e.db.QueryContext(e.ctx, q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		r := make([]string, len(cols))
		for i, v := range vals {
			r[i] = v.String
			if !v.Valid {
				r[i] = "NULL"
			}
		}
		out = append(out, r)
	}
	return out
}

func (e *env) runCopy(t *testing.T, src, dst string, structure bool) {
	t.Helper()
	tbl := e.describe(t, src)
	stmts, err := e.d.CopyTable(app(src), app(dst), tbl, structure)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		e.exec(t, s)
	}
}

func TestCopyTable(t *testing.T) {
	e := setup(t)
	e.seed(t)
	e.runCopy(t, "items", "items_copy", true)
	src, cp := e.describe(t, "items"), e.describe(t, "items_copy")
	if cp.PrimaryKey == nil || !reflect.DeepEqual(cp.PrimaryKey.Columns, src.PrimaryKey.Columns) ||
		len(cp.UniqueKeys) != len(src.UniqueKeys) || len(cp.Indexes) != len(src.Indexes) || len(cp.Checks) != len(src.Checks) {
		t.Errorf("structure lost:\n%+v\n%+v", cp, src)
	}
	if len(cp.ForeignKeys) != 0 || len(cp.Triggers) != 0 {
		t.Errorf("fks/triggers copied: %+v %+v", cp.ForeignKeys, cp.Triggers)
	}
	if cp.Options["identity.id"] != "ALWAYS" || cp.Column("total").Generated == nil {
		t.Errorf("identity/generated %+v", cp.Options)
	}
	if d := *cp.Column("seq_no").Default; !strings.Contains(d, "items_copy_seq_no_seq") {
		t.Errorf("serial default shares the source sequence: %s", d)
	}
	q := "SELECT id, seq_no, code, total, uid FROM app.%s ORDER BY id"
	if a, b := rowsOf(t, e, fmt.Sprintf(q, "items")), rowsOf(t, e, fmt.Sprintf(q, "items_copy")); !reflect.DeepEqual(a, b) || len(a) != 3 {
		t.Errorf("data differs:\n%v\n%v", a, b)
	}
	// New rows get fresh identity and serial values.
	e.exec(t, "INSERT INTO app.items_copy (code, name) VALUES ('n1', 'new')")
	r := rowsOf(t, e, "SELECT id, seq_no FROM app.items_copy WHERE code = 'n1'")
	if r[0][0] != "4" || r[0][1] != "4" {
		t.Errorf("new row ids %v", r)
	}

	e.runCopy(t, "items", "items_snap", false)
	if s := e.describe(t, "items_snap"); s.PrimaryKey != nil || len(s.Columns) != len(src.Columns) {
		t.Errorf("data-only copy %+v", s)
	}
	e.runCopy(t, "v_items", "v_items2", true)
	if v := e.describe(t, "v_items2"); v.Kind != meta.KindView || v.Options["options"] != "security_barrier=true" {
		t.Errorf("view copy %+v", v)
	}
	e.runCopy(t, "logs", "logs_copy", true)
	if r := rowsOf(t, e, "SELECT count(*) FROM app.logs_copy"); r[0][0] != "3" {
		t.Errorf("partitioned copy %v", r)
	}
}

func TestTextSearch(t *testing.T) {
	e := setup(t)
	e.seed(t)
	tbl := e.describe(t, "items")
	pred := e.d.TextSearchPredicate(tbl.Columns, e.d.Placeholder(1))
	rows := rowsOfArgs(t, e, "SELECT code FROM app.items WHERE "+pred+" ORDER BY code", "NEEDLE")
	if !reflect.DeepEqual(rows, []string{"c1", "c3"}) {
		t.Errorf("search: %v", rows)
	}
	rows = rowsOfArgs(t, e, "SELECT code FROM app.items WHERE "+pred+" AND qty > $2 ORDER BY code", "needle", 3)
	if !reflect.DeepEqual(rows, []string{"c3"}) {
		t.Errorf("search+filter: %v", rows)
	}
	if rows := rowsOfArgs(t, e, e.d.Rebind("SELECT code FROM app.items WHERE code = ? OR code = ? ORDER BY 1"), "c1", "c2"); len(rows) != 2 {
		t.Errorf("rebind %v", rows)
	}
	if rows := rowsOfArgs(t, e, "SELECT code FROM app.items WHERE "+pred, "%"); len(rows) != 0 {
		t.Errorf("wildcard matched: %v", rows)
	}
}

func rowsOfArgs(t *testing.T, e *env, q string, args ...any) []string {
	t.Helper()
	rows, err := e.db.QueryContext(e.ctx, q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func TestMapError(t *testing.T) {
	e := setup(t)
	e.seed(t)
	tbl := e.describe(t, "items")
	parent := e.describe(t, "parent")
	tests := []struct {
		name, q   string
		args      []any
		tbl       *meta.Table
		code      string
		col, cons string
	}{
		{"not null", "INSERT INTO app.items (code, name) VALUES ($1, NULL)", []any{"z1"}, tbl, "23502", "name", ""},
		{"unique", "INSERT INTO app.items (code, region, name) VALUES ($1, 'EU', 'x')", []any{"c1"}, tbl, "23505", "code", "uq_code_region"},
		{"unique index", "UPDATE app.items SET uid = (SELECT uid FROM app.items WHERE code = 'c1') WHERE code = $1", []any{"c2"}, tbl, "23505", "uid", "uq_items_uid"},
		{"fk child", "INSERT INTO app.items (code, name, parent_a, parent_b) VALUES ($1, 'x', 9, 'z')", []any{"z2"}, tbl, "23503", "parent_a", "fk_parent"},
		{"fk parent", "UPDATE app.parent SET a = 5 WHERE a = $1", []any{1}, parent, "23503", "a", "fk_parent"},
		{"check", "INSERT INTO app.items (code, name, qty) VALUES ($1, 'x', 0)", []any{"z3"}, tbl, "23514", "qty", "chk_qty"},
		{"check multi", "INSERT INTO app.items (code, name) VALUES ($1, 'x')", []any{"a;b"}, tbl, "23514", "code", "chk_code"},
		{"too long", "INSERT INTO app.items (code, name, quote_str) VALUES ('z0', 'x', $1)", []any{strings.Repeat("x", 15)}, tbl, "22001", "quote_str", ""},
		{"too long ambiguous", "INSERT INTO app.items (code, name) VALUES ($1, 'x')", []any{strings.Repeat("x", 30)}, tbl, "22001", "", ""}, // code, parent_b
		{"out of range", "INSERT INTO app.items (code, name, small) VALUES ('z4', 'x', $1)", []any{"99999"}, tbl, "22003", "small", ""},
		{"numeric overflow", "INSERT INTO app.items (code, name, price) VALUES ('z5', 'x', $1)", []any{"123456789"}, tbl, "22003", "price", ""},
		{"invalid text", "INSERT INTO app.items (code, name, small) VALUES ('z6', 'x', $1)", []any{"abc"}, tbl, "22P02", "small", ""},
		{"invalid date", "INSERT INTO app.items (code, name, day) VALUES ('z7', 'x', $1)", []any{"not a date"}, tbl, "22007", "day", ""},
		{"date overflow", "INSERT INTO app.items (code, name, day) VALUES ('z8', 'x', $1)", []any{"2024-13-45"}, tbl, "22008", "", ""}, // no type in the message: day, created
		{"identity always", "INSERT INTO app.items (id, code, name) VALUES ($1, 'z9', 'x')", []any{99}, tbl, "428C9", "id", ""},
		{"generated", "INSERT INTO app.items (total, code, name) VALUES ($1, 'z10', 'x')", []any{1}, tbl, "428C9", "total", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.db.ExecContext(e.ctx, tt.q, tt.args...)
			var pe *pgconn.PgError
			if !errors.As(err, &pe) {
				t.Fatalf("no server error: %v", err)
			}
			if pe.Code != tt.code {
				t.Errorf("code %s (%s), want %s", pe.Code, pe.Message, tt.code)
			}
			fe := e.d.MapError(err, tt.tbl)
			if fe == nil || fe.Column != tt.col || fe.Constraint != tt.cons {
				t.Errorf("%s: %+v, want col %q cons %q", pe.Message, fe, tt.col, tt.cons)
			}
		})
	}
}

// openSession opens a session.Session on itDB through the registered dialect.
func (e *env) openSession(t *testing.T) *session.Session {
	t.Helper()
	s, err := session.Open(e.ctx, e.prof, e.cred, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestCancelKeepsSession verifies that cancelling via context sends a
// cancel request and leaves the pinned connection usable.
func TestCancelKeepsSession(t *testing.T) {
	e := setup(t)
	s := e.openSession(t)
	pid := func() string {
		var p string
		if err := s.Main().QueryRowContext(e.ctx, "SELECT pg_backend_pid()::text").Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := pid()
	for _, viaQuery := range []bool{false, true} {
		ctx, done := s.StatementContext(e.ctx)
		go func() {
			time.Sleep(time.Second)
			if err := s.Cancel(e.ctx); err != nil {
				t.Error(err)
			}
		}()
		start := time.Now()
		var err error
		if viaQuery {
			var rows *sql.Rows
			rows, err = s.Main().QueryContext(ctx, "SELECT pg_sleep(30)")
			if err == nil {
				for rows.Next() {
				}
				err = rows.Err()
				rows.Close()
			}
		} else {
			_, err = s.Main().ExecContext(ctx, "SELECT pg_sleep(30)")
		}
		done(false)
		if err == nil || time.Since(start) > 10*time.Second {
			t.Fatalf("not cancelled: %v after %v", err, time.Since(start))
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code != "57014" {
			t.Errorf("error %v", err)
		}
		var one int
		if err := s.Main().QueryRowContext(e.ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("session unusable after cancel: %v", err)
		}
		if p := pid(); p != before {
			t.Errorf("connection replaced: %s -> %s", before, p)
		}
	}
	// The explicit alternative through pg_cancel_backend.
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := e.d.CancelBackend(e.ctx, s.Aux(), before); err != nil {
			t.Error(err)
		}
	}()
	if _, err := s.Main().ExecContext(e.ctx, "SELECT pg_sleep(30)"); err == nil {
		t.Error("pg_cancel_backend did not cancel")
	}
	if pid() != before {
		t.Error("connection replaced after pg_cancel_backend")
	}
}

func TestWorkspaceReconnect(t *testing.T) {
	e := setup(t)
	cfg := &config.Config{Profiles: map[string]*config.Profile{"pg": e.prof}}
	ws := session.NewWorkspace(cfg, nil)
	defer ws.CloseAll()
	os.Setenv("DBCC_IT_PG_PW", e.cred.Password)
	p, err := ws.Chdir(e.ctx, "/pg/"+itDB)
	if err != nil {
		t.Fatal(err)
	}
	if p.Database != itDB || p.Schema != "public" {
		t.Errorf("path %+v", p)
	}
	s := ws.Current()
	id1, _ := e.d.SessionID(e.ctx, s.Main())
	p, err = ws.Chdir(e.ctx, "../app")
	if err != nil || p.Schema != "app" {
		t.Fatalf("cd app: %+v %v", p, err)
	}
	if cur, _ := e.d.CurrentSchema(e.ctx, s.Main()); cur != "app" {
		t.Errorf("current schema %q", cur)
	}
	if p, err = ws.Chdir(e.ctx, ".."); err != nil || p.Schema != "" || p.Database != itDB {
		t.Errorf("cd ..: %+v %v", p, err)
	}
	if _, err := ws.Chdir(e.ctx, "nosuchschema"); err == nil {
		t.Error("cd to missing schema succeeded")
	}
	p, err = ws.Chdir(e.ctx, "/pg/"+otherDB)
	if err != nil {
		t.Fatal(err)
	}
	if p.Database != otherDB || p.Schema != "public" || s.Database() != otherDB {
		t.Errorf("after reconnect %+v %s", p, s.Database())
	}
	if db, _ := e.d.CurrentDatabase(e.ctx, s.Main()); db != otherDB {
		t.Errorf("main on %s", db)
	}
	if db, _ := e.d.CurrentDatabase(e.ctx, s.Aux()); db != otherDB {
		t.Errorf("aux on %s", db)
	}
	if id2, _ := e.d.SessionID(e.ctx, s.Main()); id2 == id1 {
		t.Error("no new backend after reconnect")
	}
	p, err = ws.Chdir(e.ctx, "/pg/"+otherDB+"/otherschema")
	if err != nil || p.Schema != "otherschema" {
		t.Fatalf("%+v %v", p, err)
	}
	var n int
	if err := s.Main().QueryRowContext(e.ctx, "SELECT count(*) FROM t").Scan(&n); err != nil {
		t.Errorf("unqualified table in otherschema: %v", err)
	}
	// Metadata of another database through AuxFor.
	aux, err := s.AuxFor(e.ctx, itDB)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := e.d.ListSchemas(e.ctx, aux, itDB)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range sch {
		found = found || o.Name == "app"
	}
	if !found {
		t.Errorf("AuxFor schemas %+v", sch)
	}
	if objs, err := e.d.ListObjects(e.ctx, aux, meta.Path{Database: itDB, Schema: "app"}, dialect.ObjectKinds{meta.KindView}); err != nil || len(objs) != 1 {
		t.Errorf("AuxFor objects %+v %v", objs, err)
	}
	// Back with cd -.
	if p, err = ws.Chdir(e.ctx, "/pg/"+itDB+"/app"); err != nil || s.Database() != itDB || p.Schema != "app" {
		t.Errorf("back: %+v %v", p, err)
	}
}

type sink struct {
	results []*exec.Result
	msgs    []exec.Message
}

func (s *sink) Result(r *exec.Result)  { s.results = append(s.results, r) }
func (s *sink) Message(m exec.Message) { s.msgs = append(s.msgs, m) }

func TestRunnerScript(t *testing.T) {
	e := setup(t)
	s := e.openSession(t)
	sk := &sink{}
	r := exec.NewRunner(s, sk)
	script := `-- headless script with dollar quotes
SET search_path TO app, public;
CREATE FUNCTION add_tax(v numeric) RETURNS numeric LANGUAGE plpgsql AS $body$
DECLARE
  rate numeric := 0.19; -- semicolons; inside
BEGIN
  RAISE NOTICE 'computing %;', v;
  RETURN round(v * (1 + rate), 2);
END;
$body$;
DO $$ BEGIN RAISE NOTICE 'hello'; END $$;
INSERT INTO parent (a, b) VALUES (7, 'q') RETURNING a, b;
CALL p_count();
SELECT add_tax(100) AS gross, E'a\'b' AS s, $x$;$x$ AS d;
`
	if err := r.RunScript(e.ctx, script, 0); err != nil {
		t.Fatal(err)
	}
	if len(sk.results) != 3 {
		t.Fatalf("results %d", len(sk.results))
	}
	last := sk.results[2].Rows()
	if len(last) != 1 || last[0][0] != "119.00" || last[0][1] != "a'b" || last[0][2] != ";" {
		t.Errorf("last result %v", last)
	}
	if ret := sk.results[0].Rows(); len(ret) != 1 || ret[0][0] != "7" {
		t.Errorf("returning %v", ret)
	}
	if cnt := sk.results[1].Rows(); len(cnt) != 1 || cnt[0][0] != "0" {
		t.Errorf("call OUT %v", cnt)
	}
	var doWarn []string
	for _, m := range sk.msgs {
		if strings.HasPrefix(m.Stmt, "DO") {
			doWarn = m.Warnings
		}
	}
	if len(doWarn) != 1 || !strings.Contains(doWarn[0], "hello") {
		t.Errorf("notices %q", doWarn)
	}
	// A failing statement stops with the right line number.
	err := r.RunScript(e.ctx, "SELECT 1;\n\nSELECT * FROM no_such_table;\nSELECT 3;", 0)
	var se *exec.StmtError
	if !errors.As(err, &se) || se.Line != 3 {
		t.Errorf("error %v", err)
	}
}
