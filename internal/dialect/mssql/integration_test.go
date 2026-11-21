//go:build integration

package mssql

// Integration tests against a real SQL Server (development only):
//
//	DBCC_IT_MSSQL=127.0.0.1:11433 DBCC_IT_MSSQL_PW=<sa password>
//	go test -tags integration -count=1 ./internal/dialect/mssql/ [-update]
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

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/session"
	"dbcc/internal/sqltext"
)

var update = flag.Bool("update", false, "rewrite golden files")

const itDB = "dbcc_it_dialect"

type env struct {
	ctx    context.Context
	d      *MSSQL
	master *sql.DB // connected to master: metadata must use three-part names
	db     *sql.DB // connected to itDB
	cred   config.Credentials
	prof   *config.Profile
}

func itCred(t *testing.T) (config.Credentials, *config.Profile) {
	t.Helper()
	addr := os.Getenv("DBCC_IT_MSSQL")
	if addr == "" {
		t.Skip("DBCC_IT_MSSQL not set")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	cred := config.Credentials{Host: host, Port: p, User: "sa", Password: os.Getenv("DBCC_IT_MSSQL_PW")}
	prof := &config.Profile{Name: "it", Driver: "mssql", Host: host, Port: p, User: "sa",
		Password: "env:DBCC_IT_MSSQL_PW", Params: map[string]string{"TrustServerCertificate": "true"}}
	return cred, prof
}

func (e *env) open(t *testing.T, db string) *sql.DB {
	t.Helper()
	d, err := e.d.Open(e.ctx, e.prof, e.cred, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	// The container may still be starting.
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := d.PingContext(e.ctx)
		if err == nil {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.db.ExecContext(e.ctx, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func runScript(ctx context.Context, c dialect.Conn, src string) error {
	stmts, err := sqltext.NewMSSQL().Split(src)
	if err != nil {
		return err
	}
	for _, st := range stmts {
		for k := 0; k < st.Count; k++ {
			if _, err := c.ExecContext(ctx, st.Text); err != nil {
				return fmt.Errorf("line %d: %w\n%s", st.Line, err, st.Text)
			}
		}
	}
	return nil
}

func dropDB(ctx context.Context, master *sql.DB, name string) {
	master.ExecContext(ctx, "IF DB_ID(@p1) IS NOT NULL BEGIN ALTER DATABASE "+(&MSSQL{}).QuoteIdent(name)+
		" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE "+(&MSSQL{}).QuoteIdent(name)+"; END", name)
}

// setup creates a fresh test database with testdata/schema.sql.
func setup(t *testing.T) *env {
	t.Helper()
	cred, prof := itCred(t)
	e := &env{ctx: context.Background(), d: &MSSQL{}, cred: cred, prof: prof}
	e.master = e.open(t, "master")
	dd, err := e.d.Detect(e.ctx, e.master)
	if err != nil {
		t.Fatal(err)
	}
	e.d = dd.(*MSSQL)
	if e.d.Major < 13 {
		t.Fatalf("server version %s too old for the tests", e.d.Version)
	}
	dropDB(e.ctx, e.master, itDB)
	if _, err := e.master.ExecContext(e.ctx, "CREATE DATABASE "+itDB+" COLLATE Latin1_General_100_CI_AS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropDB(context.Background(), e.master, itDB) })
	e.db = e.open(t, itDB)
	src, err := os.ReadFile(filepath.Join("testdata", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runScript(e.ctx, e.db, string(src)); err != nil {
		t.Fatalf("schema.sql: %v", err)
	}
	return e
}

func (e *env) seed(t *testing.T) {
	t.Helper()
	e.exec(t, `INSERT INTO dbo.parent (a, b, label) VALUES (1, N'a', N'p1'), (2, N'b', NULL)`)
	e.exec(t, `INSERT INTO sales.items (code, name, note, no_default, price, qty, parent_a, parent_b, bin, status, created) VALUES
		('c1', N'Alpha Beta', NULL, 'x', 12.50, 2, 1, N'a', 0x0102, 'active', '2024-01-02T03:04:05.678'),
		('c2', N'gamma', N'it''s', 'y', 3, 1, NULL, NULL, NULL, 'new', '2024-02-01'),
		('c3', N'DELTA needle', N'Needle in note', 'z', 0, 5, 2, N'b', NULL, 'new', '2024-03-01')`)
	e.exec(t, `INSERT INTO dbo.logs (id, msg) VALUES (1, N'a'), (2, N'b')`)
}

func (e *env) path(schema, obj string) meta.Path {
	return meta.Path{Database: itDB, Schema: schema, Object: obj}
}

func (e *env) describe(t *testing.T, schema, obj string) *meta.Table {
	t.Helper()
	tbl, err := e.d.DescribeTable(e.ctx, e.master, e.path(schema, obj))
	if err != nil {
		t.Fatalf("describe %s.%s: %v", schema, obj, err)
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
	for _, o := range [][2]string{{"dbo", "parent"}, {"sales", "items"}, {"dbo", "logs"}, {"sales", "v_items"}} {
		got, err := json.MarshalIndent(masked(e.describe(t, o[0], o[1])), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, '\n')
		file := filepath.Join("testdata", "golden", o[1]+".json")
		if *update {
			os.MkdirAll(filepath.Dir(file), 0o755)
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
			t.Errorf("%s differs from %s:\n%s", o[1], file, got)
		}
	}
	_, err := e.d.DescribeTable(e.ctx, e.master, e.path("dbo", "no_such_table"))
	if !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing table: err = %v, want ErrNotFound", err)
	}
}

func TestDescribeSemantics(t *testing.T) {
	e := setup(t)
	it := e.describe(t, "sales", "items")
	col := func(n string) meta.Column {
		c := it.Column(n)
		if c == nil {
			t.Fatalf("no column %s", n)
		}
		return *c
	}
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	checks := []struct{ what, got, want string }{
		{"id type", col("id").DataType, "int"},
		{"identity option", it.Options["identity"], "IDENTITY(100,5)"},
		{"code alias type", col("code").DataType, "[dbo].[code_t]"},
		{"note type", col("note").DataType, "nvarchar(max)"},
		{"price type", col("price").DataType, "decimal(10,2)"},
		{"created type", col("created").DataType, "datetime2(3)"},
		{"name default", str(col("name").Default), "unnamed"},
		{"price default", str(col("price").Default), "0"},
		{"rate default", str(col("rate").Default), "-1.5"},
		{"status default", str(col("status").Default), "new"},
		{"created default", str(col("created").Default), "sysutcdatetime()"},
		{"name comment", col("name").Comment, "display name"},
		{"table comment", it.Comment, "Items for sale"},
		{"total expr", col("total").Generated.Expression, "([price]*[qty])"},
		{"parent_b collation", col("parent_b").Collation, "Latin1_General_CI_AS"},
		{"price index include", strings.Join(indexNamed(it, "IX_items_price").Include, ", "), "name, qty"},
		{"price index filter", indexNamed(it, "IX_items_price").Filter, "([qty]>(0))"},
		{"fillfactor", it.Options["index IX_items_price fillfactor"], "80"},
		{"uq constraint", it.Options["index UQ_items_code constraint"], "UNIQUE"},
		{"filegroup", it.Options["filegroup"], "PRIMARY"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.what, c.got, c.want)
		}
	}
	if !col("id").AutoIncrement || col("name").DefaultExpr || !col("created").DefaultExpr || !col("order_no").DefaultExpr {
		t.Error("AutoIncrement / DefaultExpr flags wrong")
	}
	if g := col("total").Generated; g == nil || !g.Stored {
		t.Error("total is not a persisted computed column")
	}
	if g := col("label").Generated; g == nil || g.Stored {
		t.Error("label is not a virtual computed column")
	}
	if pk := it.PrimaryKey; pk == nil || pk.Name != "PK_items" || !reflect.DeepEqual(pk.Columns, []string{"id"}) {
		t.Errorf("primary key = %+v", it.PrimaryKey)
	}
	var uks []string
	for _, k := range it.UniqueKeys {
		uks = append(uks, k.Name+"("+strings.Join(k.Columns, ",")+")")
	}
	if want := []string{"UQ_items_code(code)", "UX_items_name(name,status)"}; !reflect.DeepEqual(uks, want) {
		t.Errorf("unique keys = %v, want %v (filtered UX_items_guid excluded)", uks, want)
	}
	if len(it.ForeignKeys) != 1 {
		t.Fatalf("foreign keys = %+v", it.ForeignKeys)
	}
	fk := it.ForeignKeys[0]
	if fk.OnDelete != "CASCADE" || fk.OnUpdate != "NO ACTION" || fk.RefTable.Schema != "dbo" || fk.RefTable.Object != "parent" ||
		!reflect.DeepEqual(fk.Columns, []string{"parent_a", "parent_b"}) || !reflect.DeepEqual(fk.RefColumns, []string{"a", "b"}) {
		t.Errorf("foreign key = %+v", fk)
	}
	disabled := map[string]bool{}
	for _, ck := range it.Checks {
		disabled[ck.Name] = ck.NotEnforced
	}
	if len(disabled) != 3 || !disabled["CK_items_price_qty"] || disabled["CK_items_qty"] {
		t.Errorf("checks = %+v", it.Checks)
	}
	if len(it.Triggers) != 1 || it.Triggers[0].Event != "INSERT, UPDATE" || it.Triggers[0].Timing != "AFTER" {
		t.Errorf("triggers = %+v", it.Triggers)
	}
	types := map[string]string{}
	for _, ix := range it.Indexes {
		types[ix.Name] = ix.Type
	}
	if types["CIX_items_created"] != "CLUSTERED" || types["PK_items"] != "NONCLUSTERED" {
		t.Errorf("index types = %v", types)
	}
	logs := e.describe(t, "dbo", "logs")
	if logs.Options["temporal"] != "SYSTEM_VERSIONED_TEMPORAL_TABLE" || logs.Options["history_table"] != "[dbo].[logs_history]" {
		t.Errorf("logs options = %v", logs.Options)
	}
	if g := logs.Column("valid_from").Generated; g == nil || g.Expression != "GENERATED ALWAYS AS ROW START" {
		t.Errorf("valid_from = %+v", logs.Column("valid_from"))
	}
	v := e.describe(t, "sales", "v_items")
	if v.Kind != meta.KindView || !strings.HasPrefix(v.ViewDefinition, "SELECT id, name") || v.Options["check_option"] != "CASCADED" {
		t.Errorf("view = %+v", v)
	}
}

func TestListing(t *testing.T) {
	e := setup(t)
	dbs, err := e.d.ListDatabases(e.ctx, e.master)
	if err != nil {
		t.Fatal(err)
	}
	sys := map[string]bool{}
	seenSys := false
	for _, o := range dbs {
		sys[o.Name] = o.System
		if o.System {
			seenSys = true
		} else if seenSys {
			t.Errorf("user database %s after system databases", o.Name)
		}
	}
	if !sys["master"] || !sys["tempdb"] || sys[itDB] {
		t.Errorf("databases = %+v", dbs)
	}
	if _, ok := sys[itDB]; !ok {
		t.Errorf("%s not listed", itDB)
	}
	schemas, err := e.d.ListSchemas(e.ctx, e.master, itDB)
	if err != nil {
		t.Fatal(err)
	}
	ss := map[string]bool{}
	for _, o := range schemas {
		ss[o.Name] = o.System
	}
	if ss["sales"] || ss["dbo"] || !ss["sys"] || !ss["INFORMATION_SCHEMA"] || !ss["db_owner"] || !ss["guest"] {
		t.Errorf("schemas = %+v", schemas)
	}
	list := func(schema string, kinds dialect.ObjectKinds) []string {
		objs, err := e.d.ListObjects(e.ctx, e.master, meta.Path{Database: itDB, Schema: schema}, kinds)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, o := range objs {
			out = append(out, o.Name+":"+string(o.Kind))
		}
		return out
	}
	if got, want := list("sales", nil), []string{
		"calc_total:procedure", "item_names:function", "items:table", "items_over:function",
		"order_no:sequence", "trg_items_ins:trigger", "v_items:view",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("sales objects = %v, want %v", got, want)
	}
	if got, want := list("dbo", dialect.ObjectKinds{meta.KindTable, meta.KindFunction}), []string{
		"add_tax:function", "logs:table", "logs_history:table", "parent:table",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("dbo tables+functions = %v, want %v", got, want)
	}
	objs, _ := e.d.ListObjects(e.ctx, e.master, meta.Path{Database: itDB, Schema: "sales"}, dialect.ObjectKinds{meta.KindTable})
	if len(objs) != 1 || objs[0].Comment != "Items for sale" {
		t.Errorf("items comment: %+v", objs)
	}
}

func TestObjectSource(t *testing.T) {
	e := setup(t)
	src, err := e.d.ObjectSource(e.ctx, e.master, e.path("sales", "calc_total"), meta.KindProcedure)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(src, "/* A procedure with a block comment before CREATE */\nCREATE PROCEDURE sales.calc_total") {
		t.Errorf("procedure source:\n%s", src)
	}
	seq, err := e.d.ObjectSource(e.ctx, e.master, e.path("sales", "order_no"), meta.KindSequence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seq, "START WITH 1000") || !strings.Contains(seq, "INCREMENT BY 10") || !strings.Contains(seq, "CACHE 20") {
		t.Errorf("sequence source:\n%s", seq)
	}
	// The generated sequence source runs.
	if _, err := e.db.ExecContext(e.ctx, strings.Replace(seq, "[order_no]", "[order_no2]", 1)); err != nil {
		t.Errorf("sequence source does not run: %v\n%s", err, seq)
	}
	if _, err := e.d.ObjectSource(e.ctx, e.master, e.path("sales", "calc_total"), meta.KindView); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("wrong kind: err = %v", err)
	}
	if _, err := e.d.ObjectSource(e.ctx, e.master, e.path("sales", "nope"), meta.KindView); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing: err = %v", err)
	}
}

func definition(t *testing.T, e *env, schema, name string) string {
	t.Helper()
	var def string
	if err := e.db.QueryRowContext(e.ctx, "SELECT OBJECT_DEFINITION(OBJECT_ID(@p1))", schema+"."+name).Scan(&def); err != nil {
		t.Fatal(err)
	}
	return def
}

func TestReplaceScript(t *testing.T) {
	e := setup(t)
	cases := []struct {
		schema, name string
		kind         meta.ObjectKind
		from, to     string
	}{
		{"sales", "v_items", meta.KindView, "price * qty AS amount", "price * qty * 2 AS amount"},
		{"sales", "calc_total", meta.KindProcedure, "SELECT COUNT(*) AS n", "SELECT COUNT(*) AS cnt"},
		{"dbo", "add_tax", meta.KindFunction, "1.19", "1.07"},
		{"sales", "items_over", meta.KindFunction, "price > @min", "price >= @min"},
		{"sales", "item_names", meta.KindFunction, "SELECT name FROM", "SELECT UPPER(name) FROM"},
		{"sales", "trg_items_ins", meta.KindTrigger, "N'auto'", "N'auto2'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Through the auxiliary-style connection in another database.
			script, err := e.d.ReplaceScript(e.ctx, e.master, e.path(c.schema, c.name), c.kind)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(script, "CREATE OR ALTER") {
				t.Fatalf("no CREATE OR ALTER:\n%s", script)
			}
			// Unchanged script runs (replaces the object with itself).
			if err := runScript(e.ctx, e.db, script); err != nil {
				t.Fatalf("unchanged script: %v\n%s", err, script)
			}
			changed := strings.Replace(script, c.from, c.to, 1)
			if changed == script {
				t.Fatalf("%q not in script:\n%s", c.from, script)
			}
			if err := runScript(e.ctx, e.db, changed); err != nil {
				t.Fatalf("changed script: %v\n%s", err, changed)
			}
			if def := definition(t, e, c.schema, c.name); !strings.Contains(def, c.to) {
				t.Errorf("definition not replaced:\n%s", def)
			}
			// Comments before CREATE are preserved.
			if c.name == "calc_total" && !strings.Contains(script, "/* A procedure with a block comment before CREATE */\nCREATE OR ALTER PROCEDURE [sales].[calc_total]") {
				t.Errorf("header not preserved:\n%s", script)
			}
			// In the wrong database the guard batch stops the script.
			err = runScript(e.ctx, e.master, changed)
			if err == nil || !strings.Contains(err.Error(), "switch to that database") {
				t.Errorf("wrong database: err = %v", err)
			}
		})
	}
	// The procedure still works after replacement.
	rows, err := e.db.QueryContext(e.ctx, "EXEC sales.calc_total")
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()
}

// rowsOf returns the rows of q as strings.
func rowsOf(t *testing.T, c dialect.Conn, ctx context.Context, q string, args ...any) [][]string {
	t.Helper()
	rows, err := c.QueryContext(ctx, q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		r := make([]string, len(cols))
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			r[i] = fmt.Sprint(v)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) runCopy(t *testing.T, src, dst meta.Path, structure bool) []string {
	t.Helper()
	var tbl *meta.Table
	if structure {
		var err error
		if tbl, err = e.d.DescribeTable(e.ctx, e.master, src); err != nil {
			t.Fatal(err)
		}
	}
	stmts, err := e.d.CopyTable(src, dst, tbl, structure)
	if err != nil {
		t.Fatal(err)
	}
	// Run on the connection in master: the statements are fully qualified.
	conn, err := e.master.Conn(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, s := range stmts {
		if _, err := conn.ExecContext(e.ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return stmts
}

// structure returns the comparable structure of a table: names of
// constraints that were renamed are normalised.
func structure(tbl *meta.Table, names ...string) string {
	c := *tbl
	c.Path = meta.Path{}
	c.Comment = ""
	c.ForeignKeys = nil
	c.Triggers = nil
	opts := map[string]string{}
	for k, v := range tbl.Options {
		switch {
		case k == "rows", k == "temporal", k == "history_table", strings.HasPrefix(k, "trigger "), strings.HasPrefix(k, "foreign key "):
			continue
		}
		opts[k] = v
	}
	c.Options = opts
	var cols []meta.Column
	for _, col := range tbl.Columns {
		col.Comment = ""
		if isPeriodColumn(col) {
			col.Generated = nil // copies are not system-versioned
		}
		cols = append(cols, col)
	}
	c.Columns = cols
	b, _ := json.MarshalIndent(&c, "", " ")
	out := string(b)
	for _, n := range names { // longest first: the copy's name, then the source's
		out = strings.ReplaceAll(out, n, "X")
	}
	return out
}

func TestCopyTable(t *testing.T) {
	e := setup(t)
	e.seed(t)
	for _, tc := range []struct{ schema, src, dst string }{
		{"sales", "items", "items_copy"}, {"dbo", "parent", "parent_copy"}, {"dbo", "logs", "logs_copy"},
	} {
		t.Run(tc.src, func(t *testing.T) {
			src, dst := e.path(tc.schema, tc.src), e.path(tc.schema, tc.dst)
			stmts := e.runCopy(t, src, dst, true)
			a := structure(e.describe(t, tc.schema, tc.src), tc.src)
			b := structure(e.describe(t, tc.schema, tc.dst), tc.dst, tc.src)
			if a != b {
				al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
				for i := 0; i < len(al) || i < len(bl); i++ {
					var x, y string
					if i < len(al) {
						x = al[i]
					}
					if i < len(bl) {
						y = bl[i]
					}
					if x != y {
						t.Errorf("structure differs at line %d:\n source: %s\n copy:   %s", i+1, x, y)
						break
					}
				}
				t.Logf("statements:\n%s", strings.Join(stmts, "\n"))
			}
			cols := "*"
			if tc.src == "items" {
				cols = "id, code, name, note, no_default, price, qty, total, label, created, rate, small, tiny, flag, guid, bin, parent_a, parent_b, order_no, status"
			}
			q := func(a, b string) string {
				return fmt.Sprintf("SELECT COUNT(*) FROM (SELECT %s FROM %s EXCEPT SELECT %s FROM %s) x", cols, a, cols, b)
			}
			for _, pair := range [][2]string{{e.d.Qualify(src), e.d.Qualify(dst)}, {e.d.Qualify(dst), e.d.Qualify(src)}} {
				if got := rowsOf(t, e.master, e.ctx, q(pair[0], pair[1]))[0][0]; got != "0" {
					t.Errorf("data differs: %s rows only in %s", got, pair[0])
				}
			}
			if tc.src == "items" {
				// Identity continues like the source.
				got := rowsOf(t, e.db, e.ctx, "SELECT IDENT_CURRENT('sales.items'), IDENT_CURRENT('sales.items_copy'), IDENT_INCR('sales.items_copy')")[0]
				if got[0] != got[1] || got[2] != "5" {
					t.Errorf("identity: %v", got)
				}
				// The session can enable IDENTITY_INSERT for another table again.
				e.exec(t, "SET IDENTITY_INSERT sales.items ON; SET IDENTITY_INSERT sales.items OFF")
				// Constraint names were derived from the target name.
				if n := rowsOf(t, e.db, e.ctx, "SELECT COUNT(*) FROM sys.objects WHERE name IN ('PK_items_copy', 'UQ_items_copy_code', 'CK_items_copy_qty', 'CK_items_copy_price_qty')")[0][0]; n != "4" {
					t.Errorf("derived constraint names: %s of 4\n%s", n, strings.Join(stmts, "\n"))
				}
			}
		})
	}
	t.Run("data only", func(t *testing.T) {
		e.runCopy(t, e.path("sales", "items"), e.path("dbo", "items_snap"), false)
		if n := rowsOf(t, e.db, e.ctx, "SELECT COUNT(*) FROM dbo.items_snap")[0][0]; n != "3" {
			t.Errorf("rows = %s", n)
		}
	})
	t.Run("view", func(t *testing.T) {
		e.runCopy(t, e.path("sales", "v_items"), e.path("sales", "v_items2"), true)
		v := e.describe(t, "sales", "v_items2")
		if v.Kind != meta.KindView || !strings.Contains(v.ViewDefinition, "price * qty AS amount") {
			t.Errorf("view copy = %+v", v)
		}
		if n := rowsOf(t, e.db, e.ctx, "SELECT COUNT(*) FROM sales.v_items2")[0][0]; n != "3" {
			t.Errorf("view rows = %s", n)
		}
	})
}

func TestTextSearch(t *testing.T) {
	e := setup(t)
	e.seed(t)
	tbl := e.describe(t, "sales", "items")
	for _, tc := range []struct {
		needle string
		want   int
	}{{"needle", 1}, {"ALPHA", 1}, {"12.5", 1}, {"%", 0}, {"it's", 1}, {"zzz", 0}} {
		pred := e.d.TextSearchPredicate(tbl.Columns, "@p2")
		q := "SELECT COUNT(*) FROM sales.items WHERE status <> @p1 AND " + pred
		got := rowsOf(t, e.db, e.ctx, q, "none", tc.needle)[0][0]
		if got != strconv.Itoa(tc.want) {
			t.Errorf("%q: %s rows, want %d", tc.needle, got, tc.want)
		}
	}
}

func TestLimitQuery(t *testing.T) {
	e := setup(t)
	e.seed(t)
	for _, tc := range []struct {
		q     string
		n     int
		first string
	}{
		{"SELECT name FROM sales.items ORDER BY name DESC;", 2, "gamma"},
		{"SELECT DISTINCT status FROM sales.items ORDER BY status", 1, "active"},
		{"WITH x AS (SELECT name FROM sales.items) SELECT name FROM x ORDER BY name", 1, "Alpha Beta"},
		{"SELECT TOP 3 name FROM sales.items ORDER BY name", 2, ""},
		{"SELECT name FROM sales.items UNION ALL SELECT label FROM dbo.parent ORDER BY name", 2, ""},
		{"SELECT name FROM sales.items ORDER BY name OFFSET 1 ROWS", 1, "DELTA needle"},
		{"EXEC sales.calc_total", 1, "skip"},
	} {
		lq := e.d.LimitQuery(tc.q, tc.n)
		if tc.first == "skip" {
			continue // procedures cannot be limited; documented
		}
		got := rowsOf(t, e.db, e.ctx, lq)
		if len(got) != tc.n {
			t.Errorf("%s: %d rows, want %d", lq, len(got), tc.n)
			continue
		}
		if tc.first != "" && got[0][0] != tc.first {
			t.Errorf("%s: first = %q, want %q", lq, got[0][0], tc.first)
		}
	}
}

func TestMapError(t *testing.T) {
	e := setup(t)
	e.seed(t)
	items := e.describe(t, "sales", "items")
	parent := e.describe(t, "dbo", "parent")
	ins := "INSERT INTO sales.items (code, name, no_default, status) VALUES "
	cases := []struct {
		name, q    string
		args       []any
		tbl        *meta.Table
		number     int32
		col, cons  string
		preSession string
	}{
		{"null 515", ins + "('n1', N'n1', NULL, 'new')", nil, items, 515, "no_default", "", ""},
		{"pk 2627", "SET IDENTITY_INSERT sales.items ON; INSERT INTO sales.items (id, code, name, no_default) VALUES (100, 'n2', N'n2', 'x')", nil, items, 2627, "id", "PK_items", ""},
		{"unique constraint 2627", ins + "('c1', N'n3', 'x', 'new')", nil, items, 2627, "code", "UQ_items_code", ""},
		{"unique index 2601", ins + "('n4', N'gamma', 'x', 'new')", nil, items, 2601, "name", "UX_items_name", ""},
		{"fk 547", "INSERT INTO sales.items (code, name, no_default, parent_a, parent_b) VALUES ('n5', N'n5', 'x', 9, N'z')", nil, items, 547, "parent_a", "FK_items_parent", ""},
		{"check 547 column", ins + "('n6', N'n6', 'x', 'bogus')", nil, items, 547, "status", "CK_items_status", ""},
		{"check 547 qty", "INSERT INTO sales.items (code, name, no_default, qty) VALUES ('n7', N'n7', 'x', -1)", nil, items, 547, "qty", "CK_items_qty", ""},
		{"reference 547", "UPDATE dbo.parent SET a = 5 WHERE a = 1", nil, parent, 547, "", "FK_items_parent", ""},
		{"truncated 2628", "INSERT INTO sales.items (code, name, no_default) VALUES ('n8', N'n8', 'way too long value')", nil, items, 2628, "no_default", "", ""},
		{"truncated 8152", "INSERT INTO sales.items (code, name, no_default) VALUES ('n9', N'n9', 'way too long value')", nil, items, 8152, "", "",
			"ALTER DATABASE SCOPED CONFIGURATION SET VERBOSE_TRUNCATION_WARNINGS = OFF"},
		{"overflow 220", "INSERT INTO sales.items (code, name, no_default, tiny) VALUES ('na', N'na', 'x', 300)", nil, items, 220, "", "", ""},
		{"overflow 8115", "INSERT INTO sales.items (code, name, no_default, qty) VALUES ('nb', N'nb', 'x', @p1)", []any{int64(100000)}, items, 8115, "", "", ""},
		{"conversion 245", "INSERT INTO sales.items (code, name, no_default, tiny) VALUES ('nc', N'nc', 'x', 'abc')", nil, items, 245, "", "", ""},
		{"conversion 8114", "INSERT INTO sales.items (code, name, no_default, price) VALUES ('nd', N'nd', 'x', @p1)", []any{"abc"}, items, 8114, "", "", ""},
		{"date 241", "INSERT INTO sales.items (code, name, no_default, created) VALUES ('ne', N'ne', 'x', 'not a date')", nil, items, 241, "", "", ""},
		{"date 242", "SELECT CAST('2024-13-45' AS datetime)", nil, nil, 242, "", "", ""},
		{"identity 544", "INSERT INTO sales.items (id, code, name, no_default) VALUES (1, 'nf', N'nf', 'x')", nil, items, 544, "id", "", ""},
		{"computed 271", "INSERT INTO sales.items (code, name, no_default, total) VALUES ('ng', N'ng', 'x', 1)", nil, items, 271, "total", "", ""},
		{"identity update 8102", "UPDATE sales.items SET id = 1 WHERE code = 'c1'", nil, items, 8102, "id", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, err := e.db.Conn(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if c.preSession != "" {
				if _, err := conn.ExecContext(e.ctx, c.preSession); err != nil {
					t.Fatal(err)
				}
				defer e.db.ExecContext(e.ctx, "ALTER DATABASE SCOPED CONFIGURATION SET VERBOSE_TRUNCATION_WARNINGS = ON")
			}
			_, err = conn.ExecContext(e.ctx, c.q, c.args...)
			if err == nil {
				t.Fatal("statement succeeded")
			}
			nums := []int32{}
			for _, se := range serverErrors(err) {
				nums = append(nums, se.Number)
			}
			found := false
			for _, n := range nums {
				found = found || n == c.number
			}
			if !found {
				t.Fatalf("error numbers %v, want %d: %v", nums, c.number, err)
			}
			fe := e.d.MapError(err, c.tbl)
			if fe == nil {
				t.Fatalf("MapError = nil for %v", err)
			}
			if fe.Column != c.col || fe.Constraint != c.cons {
				t.Errorf("column %q constraint %q, want %q %q (%s)", fe.Column, fe.Constraint, c.col, c.cons, fe.Message)
			}
			if len(serverErrors(fe)) == 0 {
				t.Error("FieldError does not wrap the server error")
			}
		})
	}
	if fe := e.d.MapError(errors.New("x"), items); fe != nil {
		t.Errorf("non-server error mapped: %+v", fe)
	}
}

// itConfig writes a config with profile "it" for the test server.
func itConfig(t *testing.T, db string) *config.Config {
	t.Helper()
	_, prof := itCred(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	body := fmt.Sprintf(`[profiles.it]
driver = "mssql"
host = %q
port = %d
user = "sa"
database = %q
password = "env:DBCC_IT_MSSQL_PW"
[profiles.it.params]
TrustServerCertificate = "true"
`, prof.Host, prof.Port, db)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func openSession(t *testing.T, e *env, autocommit bool) *session.Session {
	t.Helper()
	p := *e.prof
	p.Database = itDB
	if !autocommit {
		f := false
		p.Autocommit = &f
	}
	s, err := session.Open(e.ctx, &p, e.cred, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type sink struct {
	mu   sync.Mutex
	res  []*exec.Result
	msgs []exec.Message
}

func (s *sink) Result(r *exec.Result)  { s.mu.Lock(); s.res = append(s.res, r); s.mu.Unlock() }
func (s *sink) Message(m exec.Message) { s.mu.Lock(); s.msgs = append(s.msgs, m); s.mu.Unlock() }

func TestCancel(t *testing.T) {
	e := setup(t)
	s := openSession(t, e, true)
	before, err := s.Dialect.SessionID(e.ctx, s.Main())
	if err != nil {
		t.Fatal(err)
	}
	r := exec.NewRunner(s, &sink{})
	if err := r.RunScript(e.ctx, "CREATE TABLE #keep (a int)", 0); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- r.RunStatement(e.ctx, sqltext.Statement{Text: "WAITFOR DELAY '00:00:30'", Line: 1, Count: 1}, true)
	}()
	time.Sleep(time.Second)
	if err := s.Cancel(e.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled statement reported success")
		}
		t.Logf("cancelled after %v: %v", time.Since(start), err)
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the statement")
	}
	// The pinned connection is still usable and still the same session.
	var one int
	if err := s.Main().QueryRowContext(e.ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 after cancel: %v", err)
	}
	after, err := s.Dialect.SessionID(e.ctx, s.Main())
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("session changed after cancel: %s -> %s", before, after)
	}
	// Session state survives: the temp table created before cancel is still there.
	if err := s.Main().QueryRowContext(e.ctx, "SELECT COUNT(*) FROM #keep").Scan(&one); err != nil {
		t.Errorf("temp table lost after cancel: %v", err)
	}
}

func TestGoCountAndRunner(t *testing.T) {
	e := setup(t)
	s := openSession(t, e, true)
	out := &sink{}
	r := exec.NewRunner(s, out)
	script := "CREATE TABLE dbo.cnt (id int IDENTITY PRIMARY KEY, v int NULL)\nGO\n" +
		"INSERT INTO dbo.cnt (v) VALUES (1)\nGO 3\n" +
		"UPDATE dbo.cnt SET v = 2 WHERE id > 1\nGO\n" +
		"INSERT INTO dbo.cnt (v) SELECT v FROM dbo.cnt WHERE id = 1; SELECT COUNT(*) AS n FROM dbo.cnt;\nSELECT v FROM dbo.cnt ORDER BY id\nGO\n" +
		// The runner keeps only the first set of the LAST statement (lazy
		// mode: exec.Runner checks only for CALL/EXEC); earlier batches are buffered completely.
		"SELECT 1 AS last\nGO\n"
	if err := r.RunScript(e.ctx, script, 0); err != nil {
		t.Fatal(err)
	}
	if n := rowsOf(t, e.db, e.ctx, "SELECT COUNT(*) FROM dbo.cnt")[0][0]; n != "4" {
		t.Errorf("rows after GO 3 and INSERT … SELECT = %s, want 4", n)
	}
	var affected []int64
	for _, m := range out.msgs {
		affected = append(affected, m.Affected)
	}
	// CREATE, 3x INSERT, UPDATE (2 rows), mixed batch (Query: not available).
	if want := []int64{0, 1, 1, 1, 2, -1, -1}; !reflect.DeepEqual(affected, want) {
		t.Errorf("affected = %v, want %v", affected, want)
	}
	if len(out.res) != 3 {
		t.Fatalf("results = %d, want 3 (COUNT, v, last)", len(out.res))
	}
	if got := out.res[0].Rows(); len(got) != 1 || fmt.Sprint(got[0][0]) != "4" {
		t.Errorf("count result = %v", got)
	}
	if got := len(out.res[1].Rows()); got != 4 {
		t.Errorf("v result rows = %d", got)
	}
	// A failing batch stops the run with its line number.
	err := r.RunScript(e.ctx, "SELECT 1\nGO\n\nSELECT nosuchcol FROM dbo.cnt\nGO\nSELECT 2", 0)
	var se *exec.StmtError
	if !errors.As(err, &se) || se.Line != 4 {
		t.Errorf("error = %v, want line 4", err)
	}
}

func TestManualTransaction(t *testing.T) {
	e := setup(t)
	s := openSession(t, e, false)
	r := exec.NewRunner(s, &sink{})
	if err := r.RunScript(e.ctx, "INSERT INTO dbo.parent (a, b) VALUES (7, N'x')", 0); err != nil {
		t.Fatal(err)
	}
	var tc int
	if err := s.Main().QueryRowContext(e.ctx, "SELECT @@TRANCOUNT").Scan(&tc); err != nil || tc != 1 {
		t.Fatalf("@@TRANCOUNT = %d (%v), want 1 with implicit transactions", tc, err)
	}
	if !s.TxnOpen() {
		t.Error("session does not report an open transaction")
	}
	if err := r.RunScript(e.ctx, "ROLLBACK", 0); err != nil {
		t.Fatal(err)
	}
	if n := rowsOf(t, e.db, e.ctx, "SELECT COUNT(*) FROM dbo.parent WHERE a = 7")[0][0]; n != "0" {
		t.Errorf("rolled back row visible: %s", n)
	}
}

func TestChdir(t *testing.T) {
	e := setup(t)
	ws := session.NewWorkspace(itConfig(t, itDB), nil)
	defer ws.CloseAll()
	p, err := ws.Chdir(e.ctx, "/it")
	if err != nil {
		t.Fatal(err)
	}
	if p.Database != itDB || p.Schema != "dbo" {
		t.Errorf("cd /it = %+v", p)
	}
	s := ws.Current()
	cur := func() string {
		db, err := s.Dialect.CurrentDatabase(e.ctx, s.Main())
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	if p, err = ws.Chdir(e.ctx, "../sales"); err != nil || p.Schema != "sales" || p.Database != itDB {
		t.Fatalf("cd sales = %+v, %v", p, err)
	}
	if p, err = ws.Chdir(e.ctx, "/it/master"); err != nil || cur() != "master" || p.Database != "master" {
		t.Fatalf("cd /it/master = %+v, %v (server db %s)", p, err, cur())
	}
	if p, err = ws.Chdir(e.ctx, "-"); err != nil || cur() != itDB || p.Schema != "sales" {
		t.Fatalf("cd - = %+v, %v (server db %s)", p, err, cur())
	}
	if _, err = ws.Chdir(e.ctx, "/it/"+itDB+"/nosuch"); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("cd to a missing schema: %v", err)
	}
	if _, err = ws.Chdir(e.ctx, "/it/nosuchdb"); err == nil {
		t.Error("cd to a missing database succeeded")
	}
	// Metadata of another database through AuxFor.
	aux, err := s.AuxFor(e.ctx, "master")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dialect.DescribeTable(e.ctx, aux, e.path("sales", "items")); err != nil {
		t.Errorf("describe through AuxFor(master): %v", err)
	}
}

// indexNamed returns the index of t with the given name (zero value if none).
func indexNamed(t *meta.Table, name string) meta.Index {
	for _, ix := range t.Indexes {
		if ix.Name == name {
			return ix
		}
	}
	return meta.Index{}
}
