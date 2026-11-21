//go:build integration

package mysql

// Integration tests against real MySQL and MariaDB servers. Servers come
// from the environment (like internal/app/integration_test.go):
//
//	DBCC_IT_MYSQL=127.0.0.1:13306 DBCC_IT_MARIADB=127.0.0.1:13307 DBCC_IT_PW=<root password>
//	go test -tags integration -count=1 ./internal/dialect/mysql/ [-update]
//
// -update rewrites testdata/golden/<server>/<table>.json.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

var update = flag.Bool("update", false, "rewrite golden files")

const itDB = "dbcc_it_dialect"

type itServer struct{ name, addr string }

func itServers(t *testing.T) []itServer {
	var out []itServer
	for _, s := range []itServer{{"mysql", os.Getenv("DBCC_IT_MYSQL")}, {"mariadb", os.Getenv("DBCC_IT_MARIADB")}} {
		if s.addr != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Skip("no DBCC_IT_* servers configured")
	}
	return out
}

func openAs(t *testing.T, s itServer, user, pw, db string) *sql.DB {
	t.Helper()
	cfg := driver.NewConfig()
	cfg.User, cfg.Passwd = user, pw
	cfg.Net, cfg.Addr, cfg.DBName = "tcp", s.addr, db
	cfg.Timeout = 10 * time.Second
	conn, err := driver.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	d := sql.OpenDB(conn)
	t.Cleanup(func() { d.Close() })
	return d
}

func openRoot(t *testing.T, s itServer, db string) *sql.DB {
	return openAs(t, s, "root", os.Getenv("DBCC_IT_PW"), db)
}

type env struct {
	s   itServer
	ctx context.Context
	db  *sql.DB // default database itDB
	d   *MySQL
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.db.ExecContext(e.ctx, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) path(obj string) meta.Path { return meta.Path{Database: itDB, Object: obj} }

// setup creates a fresh test database with testdata/schema.sql.
func setup(t *testing.T, s itServer) *env {
	t.Helper()
	ctx := context.Background()
	admin := openRoot(t, s, "")
	for _, q := range []string{"DROP DATABASE IF EXISTS " + itDB, "CREATE DATABASE " + itDB + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+itDB) })
	e := &env{s: s, ctx: ctx, db: openRoot(t, s, itDB)}
	dd, err := (&MySQL{}).Detect(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	e.d = dd.(*MySQL)
	if e.d.MariaDB != (s.name == "mariadb") {
		t.Fatalf("%s detected as %s (%s)", s.name, e.d.Name(), e.d.Version)
	}
	files := []string{"schema.sql"}
	if !e.d.MariaDB {
		files = append(files, "schema_mysql.sql")
	}
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := runScript(ctx, e.db, string(src)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	return e
}

type execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

func runScript(ctx context.Context, c execer, src string) error {
	stmts, err := sqltext.NewMySQL().Split(src)
	if err != nil {
		return err
	}
	for _, st := range stmts {
		if _, err := c.ExecContext(ctx, st.Text); err != nil {
			return fmt.Errorf("line %d: %w\n%s", st.Line, err, st.Text)
		}
	}
	return nil
}

func (e *env) seed(t *testing.T) {
	t.Helper()
	e.exec(t, `INSERT INTO parent (a, b, label) VALUES (1, 'a', 'p1'), (2, 'b', NULL)`)
	e.exec(t, `INSERT INTO items (code, name, no_default, price, qty, parent_a, parent_b, tags, data, bin, doc, status) VALUES
		('c1', 'Alpha Beta', 'x', 12.50, 2, 1, 'a', 'a,b', X'0102', X'FF', '{"k": "Needle"}', 'active'),
		('c2', 'gamma', 'y', 3, 1, NULL, NULL, NULL, NULL, NULL, NULL, 'new'),
		('c3', 'DELTA needle', 'z', 0, 5, 2, 'b', 'c', NULL, NULL, NULL, 'it''s done')`)
	e.exec(t, `INSERT INTO logs (id, created, msg) VALUES (1, '2023-05-01', 'a'), (2, '2024-05-01', 'b'), (3, '2030-01-01', 'c')`)
}

func (e *env) describe(t *testing.T, obj string) *meta.Table {
	t.Helper()
	tbl, err := e.d.DescribeTable(e.ctx, e.db, e.path(obj))
	if err != nil {
		t.Fatalf("describe %s: %v", obj, err)
	}
	return tbl
}

func masked(tbl *meta.Table) *meta.Table {
	c := *tbl
	c.Options = map[string]string{}
	for k, v := range tbl.Options {
		switch k {
		case "rows", "auto_increment": // estimates and counters vary
			v = "<masked>"
		}
		c.Options[k] = v
	}
	return &c
}

func TestDescribeTableGolden(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			e := setup(t, s)
			for _, name := range []string{"parent", "items", "logs", "v_items"} {
				got, err := json.MarshalIndent(masked(e.describe(t, name)), "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, '\n')
				file := filepath.Join("testdata", "golden", s.name, name+".json")
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
					t.Errorf("%s differs from %s:\n%s", name, file, got)
				}
			}
			_, err := e.d.DescribeTable(e.ctx, e.db, e.path("no_such_table"))
			if !errors.Is(err, meta.ErrNotFound) {
				t.Errorf("missing table: %v", err)
			}
			// Default database from the session when Path.Database is empty.
			if tbl, err := e.d.DescribeTable(e.ctx, e.db, meta.Path{Object: "parent"}); err != nil || tbl.Path.Database != itDB {
				t.Errorf("default db: %v %+v", err, tbl)
			}
		})
	}
}

// TestDescribeSemantics asserts the normalised values independent of golden files.
func TestDescribeSemantics(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			e := setup(t, s)
			tbl := e.describe(t, "items")
			def := func(col string) (string, bool, bool) {
				c := tbl.Column(col)
				if c == nil {
					t.Fatalf("no column %s", col)
				}
				if c.Default == nil {
					return "", false, c.DefaultExpr
				}
				return *c.Default, true, c.DefaultExpr
			}
			for col, want := range map[string]struct {
				v       string
				has, ex bool
			}{
				"region": {"EU", true, false}, "note": {"NULL", true, false}, "nothing": {"", false, false},
				"no_default": {"", false, false}, "empty_str": {"", true, false}, "quote_str": {"it's", true, false},
				"price": {"0.00", true, false}, "status": {"new", true, false}, "tags": {"", false, false},
				"total": {"", false, false}, "id": {"", false, false},
			} {
				v, has, ex := def(col)
				if v != want.v || has != want.has || ex != want.ex {
					t.Errorf("%s default = %q,%v,%v want %+v", col, v, has, ex, want)
				}
			}
			for _, col := range []string{"uid", "created", "updated"} {
				if _, has, ex := def(col); !has || !ex {
					t.Errorf("%s: expression default expected", col)
				}
			}
			if c := tbl.Column("updated"); !strings.EqualFold(c.OnUpdate, "current_timestamp(3)") {
				t.Errorf("on update %q", c.OnUpdate)
			}
			if g := tbl.Column("total").Generated; g == nil || !g.Stored {
				t.Errorf("total: %+v", g)
			}
			if g := tbl.Column("name_upper").Generated; g == nil || g.Stored {
				t.Errorf("name_upper: %+v", g)
			}
			if !tbl.Column("id").AutoIncrement {
				t.Error("auto_increment")
			}
			if !reflect.DeepEqual(tbl.Column("status").EnumValues, []string{"new", "active", "it's done"}) {
				t.Errorf("enum %q", tbl.Column("status").EnumValues)
			}
			if fk := tbl.ForeignKeys; len(fk) != 1 || fk[0].OnDelete != "CASCADE" || fk[0].OnUpdate != "RESTRICT" ||
				!reflect.DeepEqual(fk[0].RefColumns, []string{"a", "b"}) || fk[0].RefTable.Database != itDB {
				t.Errorf("fk %+v", fk)
			}
			var ck *meta.Check
			for i := range tbl.Checks {
				if tbl.Checks[i].Name == "chk_code" {
					ck = &tbl.Checks[i]
				}
			}
			if ck == nil || strings.Contains(ck.Expression, `\'`) {
				t.Errorf("chk_code %+v", ck)
			}
			idx := map[string]meta.Index{}
			for _, ix := range tbl.Indexes {
				idx[ix.Name] = ix
			}
			if idx["ix_name_prefix"].Columns[0].SubPart != 10 || !idx["ix_price_desc"].Columns[0].Descending ||
				idx["ft_name"].Type != "FULLTEXT" || !idx["uq_code_region"].Unique {
				t.Errorf("indexes %+v", tbl.Indexes)
			}
			if !e.d.MariaDB && !idx["ix_func"].Columns[0].Expression {
				t.Errorf("functional index %+v", idx["ix_func"])
			}
			if len(tbl.Triggers) != 2 || tbl.Options["engine"] != "InnoDB" || tbl.Options["charset"] != "utf8mb4" || tbl.Comment != "items with everything" {
				t.Errorf("triggers/options %+v %+v", tbl.Triggers, tbl.Options)
			}
			if p := e.describe(t, "logs").Options["partitioning"]; !strings.Contains(p, "RANGE") || !strings.Contains(p, "3 partitions") {
				t.Errorf("partitioning %q", p)
			}
			v := e.describe(t, "v_items")
			if v.Kind != meta.KindView || len(v.Columns) != 3 || v.ViewDefinition == "" || v.PrimaryKey != nil {
				t.Errorf("view %+v", v)
			}
		})
	}
}

func TestObjectSource(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			e := setup(t, s)
			for name, kind := range map[string]meta.ObjectKind{
				"v_items": meta.KindView, "p_count": meta.KindProcedure, "f_double": meta.KindFunction,
				"trg_items_bi": meta.KindTrigger, "ev_cleanup": meta.KindEvent, "items": meta.KindTable,
			} {
				src, err := e.d.ObjectSource(e.ctx, e.db, e.path(name), kind)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if !strings.HasPrefix(src, "CREATE ") || !strings.Contains(src, name) {
					t.Errorf("%s: %s", name, src)
				}
			}
			_, err := e.d.ObjectSource(e.ctx, e.db, e.path("nope"), meta.KindProcedure)
			if !errors.Is(err, meta.ErrNotFound) {
				t.Errorf("missing procedure: %v", err)
			}
		})
	}
}

func TestReplaceScript(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			e := setup(t, s)
			// Run scripts on a session without a default database: the
			// script must not depend on it.
			nodb, err := openRoot(t, s, "").Conn(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer nodb.Close()
			objs := []struct {
				name string
				kind meta.ObjectKind
			}{
				{"v_items", meta.KindView}, {"p_count", meta.KindProcedure}, {"f_double", meta.KindFunction},
				{"trg_items_bi", meta.KindTrigger}, {"trg_items_au", meta.KindTrigger}, {"ev_cleanup", meta.KindEvent},
			}
			for _, o := range objs {
				before, err := e.d.ObjectSource(e.ctx, e.db, e.path(o.name), o.kind)
				if err != nil {
					t.Fatal(err)
				}
				script, err := e.d.ReplaceScript(e.ctx, e.db, e.path(o.name), o.kind)
				if err != nil {
					t.Fatalf("%s: %v", o.name, err)
				}
				stmts, _ := sqltext.NewMySQL().Split(script)
				want := 1
				if !e.d.MariaDB && o.kind != meta.KindView {
					want = 2
					if !strings.HasPrefix(stmts[0].Text, "DROP "+strings.ToUpper(string(o.kind))+" IF EXISTS `"+itDB+"`.") {
						t.Errorf("%s: first statement %q", o.name, stmts[0].Text)
					}
				}
				if len(stmts) != want {
					t.Fatalf("%s: %d statements in\n%s", o.name, len(stmts), script)
				}
				create := stmts[len(stmts)-1].Text
				if (e.d.MariaDB || o.kind == meta.KindView) != strings.HasPrefix(create, "CREATE OR REPLACE ") {
					t.Errorf("%s: %s", o.name, create)
				}
				if o.kind != meta.KindView && !strings.Contains(script, "\nDELIMITER ;\n") {
					t.Errorf("%s: no DELIMITER lines:\n%s", o.name, script)
				}
				if o.name == "p_count" && !strings.HasPrefix(script, "DELIMITER //") && !strings.Contains(script, "\nDELIMITER //\n") {
					t.Errorf("p_count body contains $$, expected //:\n%s", script)
				}
				if !strings.Contains(create, "DEFINER=") {
					t.Errorf("%s: DEFINER of the current account must be kept", o.name)
				}
				for round := 0; round < 2; round++ { // idempotent
					if err := runScript(e.ctx, nodb, script); err != nil {
						t.Fatalf("%s round %d: %v\n%s", o.name, round, err, script)
					}
				}
				after, err := e.d.ObjectSource(e.ctx, e.db, e.path(o.name), o.kind)
				if err != nil {
					t.Fatalf("%s gone after replace: %v", o.name, err)
				}
				if o.kind != meta.KindTrigger && after != before {
					t.Errorf("%s source changed:\n%s\n---\n%s", o.name, before, after)
				}
				t.Logf("%s script:\n%s", o.name, script)
			}

			// A modified script really replaces the object.
			script, _ := e.d.ReplaceScript(e.ctx, e.db, e.path("f_double"), meta.KindFunction)
			if err := runScript(e.ctx, nodb, strings.Replace(script, "x * 2", "x * 3", 1)); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := e.db.QueryRowContext(e.ctx, "SELECT f_double(2)").Scan(&n); err != nil || n != 6 {
				t.Fatalf("f_double(2) = %d, %v", n, err)
			}
			script, _ = e.d.ReplaceScript(e.ctx, e.db, e.path("trg_items_bi"), meta.KindTrigger)
			if err := runScript(e.ctx, nodb, strings.Replace(script, "'X'", "'Y'", 1)); err != nil {
				t.Fatal(err)
			}
			e.exec(t, "INSERT INTO items (code, name, no_default) VALUES ('-', 'n', 'x')")
			var code string
			e.db.QueryRowContext(e.ctx, "SELECT code FROM items WHERE name = 'n'").Scan(&code)
			if code != "Y" {
				t.Fatalf("trigger not replaced: code %q", code)
			}
		})
	}
}

// TestReplaceScriptForeignDefiner: an account without SUPER/SET_USER_ID/
// SET_ANY_DEFINER/SET USER gets a script without the foreign DEFINER, and
// the script runs.
func TestReplaceScriptForeignDefiner(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			e := setup(t, s)
			const user, pw = "dbcc_it_lim", "Lim-pw-4711"
			admin := openRoot(t, s, "")
			admin.ExecContext(e.ctx, "DROP USER IF EXISTS '"+user+"'@'%'")
			grants := []string{
				"CREATE USER '" + user + "'@'%' IDENTIFIED BY '" + pw + "'",
				"GRANT ALL PRIVILEGES ON `" + itDB + "`.* TO '" + user + "'@'%'",
			}
			if !e.d.MariaDB {
				// MySQL 8.0.20+ hides routine bodies of other definers without it.
				grants = append(grants, "GRANT SHOW_ROUTINE ON *.* TO '"+user+"'@'%'")
			}
			for _, q := range grants {
				if _, err := admin.ExecContext(e.ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP USER IF EXISTS '"+user+"'@'%'") })
			const other = "'dbcc_it_other'@'%'"
			admin.ExecContext(e.ctx, "DROP USER IF EXISTS "+other)
			if _, err := admin.ExecContext(e.ctx, "CREATE USER "+other+" IDENTIFIED BY 'Other-pw-1'"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { admin.ExecContext(context.Background(), "DROP USER IF EXISTS "+other) })
			// p_other belongs to an ordinary account that is not the caller.
			e.exec(t, "CREATE DEFINER="+other+" PROCEDURE p_other() SQL SECURITY INVOKER SELECT 42")
			lim := openAs(t, s, user, pw, itDB)

			script, err := e.d.ReplaceScript(e.ctx, lim, e.path("p_other"), meta.KindProcedure)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(script, "-- DEFINER=`dbcc_it_other`@`%` removed") {
				t.Fatalf("expected DEFINER note:\n%s", script)
			}
			stmts, _ := sqltext.NewMySQL().Split(script)
			if strings.Contains(stmts[len(stmts)-1].Text, "DEFINER=") {
				t.Fatalf("definer kept:\n%s", script)
			}
			if err := runScript(e.ctx, lim, script); err != nil {
				t.Fatalf("%v\n%s", err, script)
			}
			src, err := e.d.ObjectSource(e.ctx, e.db, e.path("p_other"), meta.KindProcedure)
			if err != nil || !strings.Contains(src, "DEFINER=`"+user+"`@`%`") {
				t.Fatalf("%v\n%s", err, src)
			}
			t.Logf("script for a foreign definer:\n%s", script)

			// Keeping the foreign DEFINER fails for this account with 1227,
			// which is why it is removed.
			e.exec(t, "DROP PROCEDURE p_other")
			e.exec(t, "CREATE DEFINER="+other+" PROCEDURE p_other() SQL SECURITY INVOKER SELECT 42")
			kept := strings.Replace(src, "DEFINER=`"+user+"`@`%`", "DEFINER="+other, 1)
			if e.d.MariaDB {
				_, err = lim.ExecContext(e.ctx, "CREATE OR REPLACE"+kept[len("CREATE"):])
			} else {
				lim.ExecContext(e.ctx, "DROP PROCEDURE p_other")
				_, err = lim.ExecContext(e.ctx, kept)
			}
			if errNumber(err) != 1227 {
				t.Fatalf("expected 1227 for a foreign DEFINER, got %v", err)
			}

			// Objects defined by root: MySQL 8 protects SYSTEM_USER definers, so
			// the DROP fails first and the object survives; MariaDB replaces it
			// with the caller as definer.
			script, err = e.d.ReplaceScript(e.ctx, lim, e.path("p_count"), meta.KindProcedure)
			if err != nil {
				t.Fatal(err)
			}
			err = runScript(e.ctx, lim, script)
			if !e.d.MariaDB {
				if errNumber(err) != 1227 || !strings.Contains(err.Error(), "SYSTEM_USER") || !strings.Contains(err.Error(), "DROP PROCEDURE") {
					t.Fatalf("expected SYSTEM_USER 1227 on DROP, got %v", err)
				}
				if _, err := e.d.ObjectSource(e.ctx, e.db, e.path("p_count"), meta.KindProcedure); err != nil {
					t.Fatalf("p_count lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
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
			r[i] = "NULL"
			if v.Valid {
				r[i] = "'" + v.String
			}
		}
		out = append(out, r)
	}
	return out
}

func (e *env) runCopy(t *testing.T, src, dst string, structure bool) {
	t.Helper()
	var tbl *meta.Table
	if structure {
		tbl = e.describe(t, src)
	}
	stmts, err := e.d.CopyTable(e.path(src), e.path(dst), tbl, structure)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range stmts {
		e.exec(t, q)
	}
}

func checkExprs(tbl *meta.Table) []string {
	var out []string
	for _, c := range tbl.Checks {
		out = append(out, c.Expression)
	}
	sort.Strings(out)
	return out
}

func TestCopyTable(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			e := setup(t, s)
			e.seed(t)

			e.runCopy(t, "items", "items_copy", true)
			a, b := e.describe(t, "items"), e.describe(t, "items_copy")
			if !reflect.DeepEqual(a.Columns, b.Columns) {
				t.Errorf("columns differ:\n%+v\n%+v", a.Columns, b.Columns)
			}
			if !reflect.DeepEqual(a.PrimaryKey, b.PrimaryKey) || !reflect.DeepEqual(a.UniqueKeys, b.UniqueKeys) {
				t.Errorf("keys differ")
			}
			// Indexes: same, except the index MySQL/MariaDB created for the FK
			// is kept by LIKE as a plain index (it is declared explicitly here).
			if !reflect.DeepEqual(a.Indexes, b.Indexes) {
				t.Errorf("indexes differ:\n%+v\n%+v", a.Indexes, b.Indexes)
			}
			if !reflect.DeepEqual(checkExprs(a), checkExprs(b)) {
				t.Errorf("checks differ: %v / %v", a.Checks, b.Checks)
			}
			if len(b.ForeignKeys) != 0 || len(a.ForeignKeys) != 1 {
				t.Errorf("A1: foreign keys must not be copied: %+v", b.ForeignKeys)
			}
			if len(b.Triggers) != 0 {
				t.Errorf("triggers copied: %+v", b.Triggers)
			}
			q := "SELECT * FROM %s ORDER BY id"
			if ra, rb := rowsOf(t, e, fmt.Sprintf(q, "items")), rowsOf(t, e, fmt.Sprintf(q, "items_copy")); len(ra) != 3 || !reflect.DeepEqual(ra, rb) {
				t.Errorf("data differs:\n%v\n%v", ra, rb)
			}

			e.runCopy(t, "logs", "logs_copy", true)
			if pa, pb := e.describe(t, "logs").Options["partitioning"], e.describe(t, "logs_copy").Options["partitioning"]; pa != pb || pa == "" {
				t.Errorf("partitioning %q / %q", pa, pb)
			}
			if len(rowsOf(t, e, "SELECT * FROM logs_copy")) != 3 {
				t.Error("logs rows")
			}

			e.runCopy(t, "v_items", "v_items_copy", true) // A2: second view
			if v := e.describe(t, "v_items_copy"); v.Kind != meta.KindView {
				t.Errorf("cp -a view: kind %s", v.Kind)
			}
			if ra, rb := rowsOf(t, e, "SELECT * FROM v_items ORDER BY id"), rowsOf(t, e, "SELECT * FROM v_items_copy ORDER BY id"); len(ra) != 2 || !reflect.DeepEqual(ra, rb) {
				t.Errorf("view data %v / %v", ra, rb)
			}

			e.runCopy(t, "v_items", "v_snap", false) // A2: snapshot table
			e.runCopy(t, "items", "items_snap", false)
			for _, n := range []string{"v_snap", "items_snap"} {
				if tb := e.describe(t, n); tb.Kind != meta.KindTable {
					t.Errorf("%s kind %s", n, tb.Kind)
				}
			}
			if ra, rb := rowsOf(t, e, "SELECT * FROM v_items ORDER BY id"), rowsOf(t, e, "SELECT * FROM v_snap ORDER BY id"); !reflect.DeepEqual(ra, rb) {
				t.Errorf("snapshot data %v / %v", ra, rb)
			}
		})
	}
}

func TestTextSearch(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			e := setup(t, s)
			e.seed(t)
			tbl := e.describe(t, "items")
			pred := e.d.TextSearchPredicate(tbl.Columns, "?")
			n := len(e.d.TextSearchColumns(tbl.Columns))
			if n != strings.Count(pred, "?") || n == 0 {
				t.Fatalf("%d columns, predicate %s", n, pred)
			}
			for text, want := range map[string]string{
				"needle": "c1,c3", // JSON doc (c1), utf8mb4_bin name (c3), case-insensitive
				"ALPHA":  "c1",
				"12.5":   "c1", // decimal via CAST
				"%":      "",   // literal, no LIKE wildcard
				"done":   "c3", // enum value
				"gamma":  "c2",
			} {
				args := make([]any, n)
				for i := range args {
					args[i] = text
				}
				rows, err := e.db.QueryContext(e.ctx, "SELECT code FROM items WHERE "+pred+" ORDER BY code", args...)
				if err != nil {
					t.Fatalf("%v\n%s", err, pred)
				}
				var got []string
				for rows.Next() {
					var c string
					rows.Scan(&c)
					got = append(got, c)
				}
				rows.Close()
				if strings.Join(got, ",") != want {
					t.Errorf("%q: got %v want %s", text, got, want)
				}
			}
		})
	}
}

func TestMapError(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			e := setup(t, s)
			e.seed(t)
			items, parent := e.describe(t, "items"), e.describe(t, "parent")
			ins := "INSERT INTO items (code, name, no_default%s) VALUES ('%s', 'n', 'x'%s)"
			colCheck := "items_chk_1"
			if e.d.MariaDB {
				colCheck = "qty"
			}
			for _, c := range []struct {
				name           string
				q              string
				tbl            *meta.Table
				mysql, mariadb uint16
				col, cons      string
			}{
				{"not null", "INSERT INTO items (code, name, no_default) VALUES (NULL, 'n', 'x')", items, 1048, 1048, "code", ""},
				{"no default", "INSERT INTO items (code, name) VALUES ('q1', 'n')", items, 1364, 1364, "no_default", ""},
				{"too long", fmt.Sprintf(ins, ", region", "q2", ", 'EUR'"), items, 1406, 1406, "region", ""},
				{"out of range", fmt.Sprintf(ins, ", qty", "q3", ", 99999999999"), items, 1264, 1264, "qty", ""},
				{"incorrect integer", fmt.Sprintf(ins, ", qty", "q4", ", 'abc'"), items, 1366, 1366, "qty", ""},
				{"incorrect datetime", fmt.Sprintf(ins, ", created", "q5", ", 'notadate'"), items, 1292, 1292, "created", ""},
				{"truncated enum", fmt.Sprintf(ins, ", status", "q6", ", 'bogus'"), items, 1265, 1265, "status", ""},
				{"duplicate", fmt.Sprintf(ins, "", "c1", ""), items, 1062, 1062, "code", "uq_code_region"},
				{"duplicate pk", "INSERT INTO parent (a, b) VALUES (1, 'a')", parent, 1062, 1062, "a", "PRIMARY"},
				{"fk child", fmt.Sprintf(ins, ", parent_a, parent_b", "q7", ", 9, 'z'"), items, 1452, 1452, "parent_a", "fk_items_parent"},
				{"fk parent", "UPDATE parent SET a = 5 WHERE a = 1", parent, 1451, 1451, "a", "fk_items_parent"},
				{"check table", fmt.Sprintf(ins, ", price", "q8", ", -1"), items, 3819, 4025, "price", "chk_price"},
				{"check column", fmt.Sprintf(ins, ", qty", "q9", ", 200000"), items, 3819, 4025, "qty", colCheck},
				{"generated", fmt.Sprintf(ins, ", total", "q10", ", 5"), items, 3105, 1906, "total", ""},
			} {
				_, err := e.db.ExecContext(e.ctx, c.q)
				want := c.mysql
				if e.d.MariaDB {
					want = c.mariadb
				}
				if got := errNumber(err); got != want {
					t.Errorf("%s: error %d (%v), want %d", c.name, got, err, want)
					continue
				}
				fe := e.d.MapError(err, c.tbl)
				if fe == nil || fe.Column != c.col || fe.Constraint != c.cons {
					t.Errorf("%s: %v → %+v, want column %q constraint %q", c.name, err, fe, c.col, c.cons)
					continue
				}
				t.Logf("%-18s %d %-12s %-16s %s", c.name, want, fe.Column, fe.Constraint, fe.Message)
			}
		})
	}
}

func grantsOf(t *testing.T, db *sql.DB, acct string) []string {
	t.Helper()
	rows, err := db.Query("SHOW GRANTS FOR " + acct)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		rows.Scan(&g)
		out = append(out, g)
	}
	return out
}

func hasProcGrant(gs []string) bool {
	for _, g := range gs {
		if strings.Contains(g, "EXECUTE ON PROCEDURE") {
			return true
		}
	}
	return false
}

// TestA21RoutinePrivileges verifies spec assumption A21: does replacing a
// routine through ReplaceScript keep privileges granted on it?
// Result (MySQL 8.4): with automatic_sp_privileges=1 (the default) DROP
// PROCEDURE removes all rows for the routine from mysql.procs_priv, so the
// GRANT is lost; with automatic_sp_privileges=0 the grant survives the
// DROP + CREATE. MariaDB's CREATE OR REPLACE keeps it in both settings.
func TestA21RoutinePrivileges(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			e := setup(t, s)
			const acct = "'dbcc_it_a21'@'%'"
			admin := openRoot(t, s, "")
			admin.Exec("DROP USER IF EXISTS " + acct)
			if _, err := admin.Exec("CREATE USER " + acct + " IDENTIFIED BY 'A21-pw-0815'"); err != nil {
				t.Fatal(err)
			}
			var orig int
			admin.QueryRow("SELECT @@GLOBAL.automatic_sp_privileges").Scan(&orig)
			t.Cleanup(func() {
				admin.Exec("DROP USER IF EXISTS " + acct)
				admin.Exec(fmt.Sprintf("SET GLOBAL automatic_sp_privileges = %d", orig))
			})
			for _, auto := range []int{1, 0} {
				if _, err := admin.Exec(fmt.Sprintf("SET GLOBAL automatic_sp_privileges = %d", auto)); err != nil {
					t.Fatal(err)
				}
				if _, err := admin.Exec("GRANT EXECUTE ON PROCEDURE `" + itDB + "`.`p_count` TO " + acct); err != nil {
					t.Fatal(err)
				}
				before := grantsOf(t, admin, acct)
				if !hasProcGrant(before) {
					t.Fatalf("grant missing before: %v", before)
				}
				script, err := e.d.ReplaceScript(e.ctx, e.db, e.path("p_count"), meta.KindProcedure)
				if err != nil {
					t.Fatal(err)
				}
				if err := runScript(e.ctx, e.db, script); err != nil {
					t.Fatal(err)
				}
				after := grantsOf(t, admin, acct)
				var n int
				admin.QueryRow("SELECT COUNT(*) FROM mysql.procs_priv WHERE User = 'dbcc_it_a21' AND Routine_name = 'p_count'").Scan(&n)
				t.Logf("%s %s automatic_sp_privileges=%d: before %q after %q procs_priv rows=%d",
					s.name, e.d.Version, auto, before, after, n)
				kept := hasProcGrant(after)
				if e.d.MariaDB && !kept {
					t.Errorf("MariaDB CREATE OR REPLACE lost the grant")
				}
				if !e.d.MariaDB && kept != (auto == 0) {
					t.Errorf("MySQL automatic_sp_privileges=%d: grant kept=%v (A21 finding changed)", auto, kept)
				}
			}
		})
	}
}
