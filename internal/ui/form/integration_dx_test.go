//go:build integration

package form

// The statements of the form against PostgreSQL and SQL Server, through
// exec.Runner as in the application (numbered placeholders, DEFAULT
// VALUES, MapError):
//
//	DBCC_IT_POSTGRES=127.0.0.1:15432 DBCC_IT_PG_PW=<postgres password>
//	DBCC_IT_MSSQL=127.0.0.1:11433    DBCC_IT_MSSQL_PW=<sa password>

import (
	"context"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/session"

	_ "dbcc/internal/dialect/mssql"
	_ "dbcc/internal/dialect/postgres"
)

type dxServer struct {
	name, env, user, pwEnv, adminDB, schema, drop string
	params                                        map[string]string
	ddl                                           []string
	ph                                            string // placeholder prefix
}

var dxServers = []dxServer{
	{name: "postgres", env: "DBCC_IT_POSTGRES", user: "postgres", pwEnv: "DBCC_IT_PG_PW", adminDB: "postgres", schema: "public",
		drop: "DROP DATABASE IF EXISTS " + itDB + " WITH (FORCE)", params: map[string]string{"sslmode": "disable"}, ph: "$",
		ddl: []string{
			`CREATE TABLE parent (a int NOT NULL, b varchar(20) NOT NULL, label varchar(40), PRIMARY KEY (a, b))`,
			`CREATE TABLE items (
			   id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			   code varchar(20) NOT NULL,
			   region char(2) NOT NULL DEFAULT 'EU',
			   note varchar(40) DEFAULT 'dflt',
			   qty int NOT NULL DEFAULT 1,
			   flag boolean,
			   total int GENERATED ALWAYS AS (qty * 2) STORED,
			   parent_a int, parent_b varchar(20),
			   CONSTRAINT uq_code_region UNIQUE (code, region),
			   CONSTRAINT fk_items_parent FOREIGN KEY (parent_a, parent_b) REFERENCES parent (a, b),
			   CONSTRAINT chk_qty CHECK (qty >= 0))`,
		}},
	{name: "mssql", env: "DBCC_IT_MSSQL", user: "sa", pwEnv: "DBCC_IT_MSSQL_PW", adminDB: "master", schema: "dbo",
		drop: "IF DB_ID('" + itDB + "') IS NOT NULL BEGIN ALTER DATABASE " + itDB +
			" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE " + itDB + " END",
		params: map[string]string{"TrustServerCertificate": "true"}, ph: "@p",
		ddl: []string{
			`CREATE TABLE parent (a int NOT NULL, b varchar(20) NOT NULL, label varchar(40), CONSTRAINT PK_parent PRIMARY KEY (a, b))`,
			`CREATE TABLE items (
			   id int IDENTITY(1,1) CONSTRAINT PK_items PRIMARY KEY,
			   code varchar(20) NOT NULL,
			   region char(2) NOT NULL CONSTRAINT DF_region DEFAULT 'EU',
			   note varchar(40) CONSTRAINT DF_note DEFAULT 'dflt',
			   qty int NOT NULL CONSTRAINT DF_qty DEFAULT 1,
			   flag bit,
			   total AS (qty * 2) PERSISTED,
			   parent_a int, parent_b varchar(20),
			   CONSTRAINT uq_code_region UNIQUE (code, region),
			   CONSTRAINT fk_items_parent FOREIGN KEY (parent_a, parent_b) REFERENCES parent (a, b),
			   CONSTRAINT chk_qty CHECK (qty >= 0))`,
		}},
}

func (s dxServer) open(t *testing.T, db string) *session.Session {
	t.Helper()
	host, portS, err := net.SplitHostPort(os.Getenv(s.env))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portS)
	prof := &config.Profile{Name: "it", Driver: s.name, Host: host, Port: port, User: s.user, Params: s.params}
	sess, err := session.Open(context.Background(), prof, config.Credentials{Host: host, Port: port, User: s.user, Password: os.Getenv(s.pwEnv)}, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// runnerBackend is the form Backend as the application builds it: all
// statements through exec.Runner on the main session.
type runnerBackend struct {
	r *exec.Runner
}

func (b *runnerBackend) Exec(q string, args ...any) (int64, error) {
	return b.r.Exec(context.Background(), q, args...)
}
func (b *runnerBackend) Query(q string, args ...any) (*exec.Result, error) {
	return b.r.Query(context.Background(), q, args...)
}
func (b *runnerBackend) Grid(q string, args ...any) (*exec.Result, error) {
	res, err := b.r.Grid(context.Background(), q, nil, args...)
	if err == nil {
		err = res.FetchAll(100)
	}
	return res, err
}
func (b *runnerBackend) Dialect() dialect.Dialect { return b.r.Sess.Dialect }
func (b *runnerBackend) Describe(p meta.Path) (*meta.Table, error) {
	return b.r.Sess.Dialect.DescribeTable(context.Background(), b.r.Sess.Aux(), p)
}

func TestIntegrationStatementsDX(t *testing.T) {
	ran := false
	for _, s := range dxServers {
		if os.Getenv(s.env) == "" {
			continue
		}
		ran = true
		t.Run(s.name, func(t *testing.T) { runStatementsDX(t, s) })
	}
	if !ran {
		t.Skip("no DBCC_IT_POSTGRES / DBCC_IT_MSSQL servers configured")
	}
}

func runStatementsDX(t *testing.T, s dxServer) {
	ctx := context.Background()
	admin := s.open(t, s.adminDB)
	for _, q := range []string{s.drop, "CREATE DATABASE " + itDB} {
		if _, err := admin.Main().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { admin.Main().ExecContext(context.Background(), s.drop) })
	sess := s.open(t, itDB)
	r := exec.NewRunner(sess, nil)
	for _, q := range append(s.ddl, "INSERT INTO parent VALUES (1, 'a', 'one'), (2, 'b', 'two')") {
		if _, err := r.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	be := &runnerBackend{r: r}
	d := sess.Dialect
	items, err := be.Describe(meta.Path{Database: itDB, Schema: s.schema, Object: "items"})
	if err != nil {
		t.Fatal(err)
	}
	ins := func(fs ...FieldState) error {
		t.Helper()
		q, args, err := BuildInsert(d, items, fs)
		if err != nil {
			t.Fatal(err)
		}
		_, err = be.Exec(q, args...)
		return err
	}
	v := func(col, val string) FieldState { return FieldState{Column: col, State: Value, Value: val} }
	n := func(col string) FieldState { return FieldState{Column: col, State: Null} }

	if q, _, _ := BuildInsert(d, items, []FieldState{v("code", "x"), v("qty", "1")}); !strings.Contains(q, "VALUES ("+s.ph+"1, "+s.ph+"2)") {
		t.Errorf("placeholders: %s", q)
	}
	if err := ins(v("code", "c1"), v("flag", "1"), v("parent_a", "1"), v("parent_b", "a"), n("note")); err != nil {
		t.Fatal(err)
	}
	if err := ins(v("code", ""), v("note", "")); err != nil {
		t.Fatal(err)
	}
	res, err := be.Query("SELECT code, region, note, qty, total, parent_a, parent_b FROM items ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	want := []exec.Row{{"c1", "EU", nil, "1", "2", "1", "a"}, {"", "EU", "", "1", "2", nil, nil}}
	if got := res.Rows(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows\n%v\nwant\n%v", got, want)
	}
	if res, _ := be.Query("SELECT COUNT(*) FROM items WHERE flag = "+d.Placeholder(1), true); res == nil || res.Rows()[0][0] != "1" {
		t.Errorf("boolean field not stored as true: %v", res)
	}

	// Mapped server errors.
	for _, c := range []struct {
		name string
		err  error
		cols []string
	}{
		{"not null", ins(n("code")), []string{"code"}},
		{"all defaults, code has none", ins(), []string{"code"}},
		{"duplicate", ins(v("code", "c1")), []string{"code", "region"}},
		{"fk", ins(v("code", "c9"), v("parent_a", "9"), v("parent_b", "z")), []string{"parent_a", "parent_b"}},
		{"check", ins(v("code", "c8"), v("qty", "-1")), []string{"qty"}},
	} {
		if c.err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		fe := d.MapError(c.err, items)
		if fe == nil {
			t.Errorf("%s: %v not mapped", c.name, c.err)
			continue
		}
		cols := items.ConstraintColumns(fe.Constraint)
		if len(cols) == 0 && fe.Column != "" {
			cols = []string{fe.Column}
		}
		if !reflect.DeepEqual(cols, c.cols) && !(c.name == "check" && strings.EqualFold(fe.Constraint, "chk_qty")) {
			t.Errorf("%s: %+v → %v, want %v", c.name, fe, cols, c.cols)
		}
	}

	// UPDATE on the original key, only changed fields.
	q, args, err := BuildUpdate(d, items, []FieldState{
		{Column: "code", State: Value, Value: "c2", HasOrig: true, Orig: "c1"},
		{Column: "qty", State: Value, Value: "1", HasOrig: true, Orig: "1"},
		{Column: "note", State: Value, Value: "x", HasOrig: true, Orig: nil},
	}, []KeyValue{{Column: "code", Value: "c1"}, {Column: "region", Value: "EU"}})
	if err != nil {
		t.Fatal(err)
	}
	wantQ := "UPDATE " + d.Qualify(items.Path) + " SET " + d.QuoteIdent("code") + " = " + s.ph + "1, " + d.QuoteIdent("note") + " = " + s.ph + "2 WHERE " +
		d.QuoteIdent("code") + " = " + s.ph + "3 AND " + d.QuoteIdent("region") + " = " + s.ph + "4"
	if q != wantQ {
		t.Fatalf("update\n%s\nwant\n%s", q, wantQ)
	}
	if a, err := be.Exec(q, args...); err != nil || a != 1 {
		t.Fatalf("update: %d %v", a, err)
	}
	if a, err := be.Exec(q, args...); err != nil || a != 0 {
		t.Fatalf("second update: %d %v", a, err)
	}
	q, args, _ = BuildUpdate(d, items, []FieldState{{Column: "code", State: Value, Value: "", HasOrig: true, Orig: "c2"}}, []KeyValue{{Column: "id", Value: "1"}})
	if _, err := be.Exec(q, args...); err == nil {
		t.Fatal("duplicate update must fail")
	} else if fe := d.MapError(err, items); fe == nil || !strings.EqualFold(fe.Constraint, "uq_code_region") {
		t.Fatalf("duplicate update: %v → %+v", err, fe)
	}

	// End to end: the lookup (quick search with the dialect's placeholder)
	// fills the composite FK; an FK violation marks both FK fields.
	f := New(Options{Table: items, Backend: be})
	f.FocusField("code")
	for _, ch := range "e2e" {
		f.HandleKey(tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone))
	}
	f.FocusField("parent_a")
	f.HandleKey(tcell.NewEventKey(tcell.KeyF4, 0, tcell.ModNone))
	if f.look == nil {
		t.Fatalf("lookup: %s", f.Message())
	}
	for _, ch := range "two" {
		f.HandleKey(tcell.NewEventKey(tcell.KeyRune, ch, tcell.ModNone))
	}
	if src := f.look.grid.Source(); src == nil || src.Len() != 1 {
		t.Fatalf("lookup filter: %v %s", src, f.look.status)
	}
	f.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if a, b := f.fieldOf("parent_a"), f.fieldOf("parent_b"); a.value != "2" || b.value != "b" || a.desc != "two" {
		t.Fatalf("lookup selection %+v %+v", a, b)
	}
	if _, err := be.Exec("DELETE FROM parent WHERE a = 2"); err != nil {
		t.Fatal(err)
	}
	f.Save(false)
	if f.Closed() || f.fieldOf("parent_a").err == "" || f.fieldOf("parent_b").err == "" {
		t.Fatalf("fk error not marked: %s", f.Message())
	}
	t.Logf("%s: %s", s.name, f.Message())
}
