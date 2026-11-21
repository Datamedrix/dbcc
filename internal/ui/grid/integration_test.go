//go:build integration

package grid

// Server-side sort and filter on a real table through exec.Runner.Grid as
// Requery (spec 10.4, 10.5, M6) on every server, with each dialect's bind
// placeholders. Servers come from the environment (see itServer):
//
//	go test -tags integration -count=1 ./internal/ui/grid/

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
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/session"

	_ "dbcc/internal/dialect/mssql"
	_ "dbcc/internal/dialect/mysql"
	_ "dbcc/internal/dialect/postgres"
)

const itGridDB = "dbcc_it_grid"

// itServer describes one test server (spec 16): MySQL and MariaDB from
// DBCC_IT_MYSQL / DBCC_IT_MARIADB (root, DBCC_IT_PW), PostgreSQL from
// DBCC_IT_POSTGRES (postgres, DBCC_IT_PG_PW), SQL Server from
// DBCC_IT_MSSQL (sa, DBCC_IT_MSSQL_PW).
type itServer struct {
	name, driver, env, user, pwEnv, adminDB, schema string
	params                                          map[string]string
}

var itServers = []itServer{
	{"mysql", "mysql", "DBCC_IT_MYSQL", "root", "DBCC_IT_PW", "", "", nil},
	{"mariadb", "mysql", "DBCC_IT_MARIADB", "root", "DBCC_IT_PW", "", "", nil},
	{"postgres", "postgres", "DBCC_IT_POSTGRES", "postgres", "DBCC_IT_PG_PW", "postgres", "public", map[string]string{"sslmode": "disable"}},
	{"mssql", "mssql", "DBCC_IT_MSSQL", "sa", "DBCC_IT_MSSQL_PW", "master", "dbo", map[string]string{"TrustServerCertificate": "true"}},
}

func (s itServer) open(t *testing.T, db string) *session.Session {
	t.Helper()
	host, portS, err := net.SplitHostPort(os.Getenv(s.env))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portS)
	prof := &config.Profile{Name: "it", Driver: s.driver, Host: host, Port: port, User: s.user, Params: s.params}
	sess, err := session.Open(context.Background(), prof, config.Credentials{Host: host, Port: port, User: s.user, Password: os.Getenv(s.pwEnv)}, db)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func (s itServer) dropDB(db string) string {
	switch s.driver {
	case "postgres":
		return "DROP DATABASE IF EXISTS " + db + " WITH (FORCE)"
	case "mssql":
		return "IF DB_ID('" + db + "') IS NOT NULL BEGIN ALTER DATABASE " + db + " SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE " + db + " END"
	}
	return "DROP DATABASE IF EXISTS " + db
}

// createDB creates a fresh database db and returns a session in it; the
// database is dropped at the end of the test.
func (s itServer) createDB(t *testing.T, db string) *session.Session {
	t.Helper()
	ctx := context.Background()
	admin := s.open(t, s.adminDB)
	for _, q := range []string{s.dropDB(db), "CREATE DATABASE " + db} {
		if _, err := admin.Main().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		admin.Main().ExecContext(context.Background(), s.dropDB(db))
		admin.Close()
	})
	sess := s.open(t, db)
	t.Cleanup(func() { sess.Close() })
	return sess
}

func TestIntegrationServerSideSortFilter(t *testing.T) {
	ran := false
	for _, s := range itServers {
		if os.Getenv(s.env) == "" {
			continue
		}
		ran = true
		t.Run(s.name, func(t *testing.T) { runServerSideIT(t, s) })
	}
	if !ran {
		t.Skip("no DBCC_IT_* servers configured")
	}
}

func runServerSideIT(t *testing.T, srv itServer) {
	ctx := context.Background()
	sess := srv.createDB(t, itGridDB)
	run := exec.NewRunner(sess, nil)
	run.FetchSize = 3 // lazy loading after every re-query
	d := sess.Dialect
	obj := meta.Path{Database: itGridDB, Schema: srv.schema, Object: "customers"}
	weird := d.QuoteIdent("we`ird")
	for _, q := range []string{
		"CREATE TABLE " + d.Qualify(obj) + " (id INT PRIMARY KEY, name VARCHAR(40) NOT NULL, city VARCHAR(40) NULL, " + weird + " INT NULL)",
		"INSERT INTO " + d.Qualify(obj) + " VALUES (1,'Bob','Berlin',5),(2,'alice','Paris',NULL),(3,'Carol','Bern',7)," +
			"(4,'dave',NULL,1),(5,'Eve','Bergen',2),(6,'frank','Rome',9),(7,'Gina','Berlin',3),(8,'hank','Oslo',4)",
	} {
		if _, err := run.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	tbl, err := sess.Dialect.DescribeTable(ctx, sess.Aux(), obj)
	if err != nil {
		t.Fatal(err)
	}
	origin := &exec.Origin{Object: obj, SQL: sess.Dialect.Qualify(obj), Table: tbl}
	res, err := run.Grid(ctx, "SELECT * FROM "+origin.SQL, origin)
	if err != nil {
		t.Fatal(err)
	}
	var errs []error
	var sqls []string
	g := New(res, Options{
		FetchSize: 3,
		Dialect:   sess.Dialect,
		Requery: func(q string, args []any) (*exec.Result, error) {
			sqls = append(sqls, q)
			return run.Grid(ctx, q, origin, args...)
		},
		OnError: func(e error) { errs = append(errs, e) },
	})
	if !g.TableOrigin() {
		t.Fatal("not table-origin")
	}
	ids := func() []string {
		g.Result().FetchAll(100)
		var out []string
		for _, r := range g.Result().Rows() {
			out = append(out, r[0].(string))
		}
		return out
	}

	// Sort: name DESC (case-insensitive collation), server-side.
	key(g, tcell.KeyRight)
	g.Action("sort")
	g.Action("sort")
	if got := ids(); !reflect.DeepEqual(got, []string{"8", "7", "6", "5", "4", "3", "1", "2"}) {
		t.Fatalf("name desc: %v (%s)", got, sqls)
	}
	if g.CurrentColumn() != 1 {
		t.Fatalf("cursor column %d", g.CurrentColumn())
	}

	// Lazy loading restarts on the new result: 3 rows, then more on End.
	g.SetSearch("BER") // contains, case-insensitive, all columns
	if n := g.Result().Len(); n != 3 || !g.Result().More() {
		t.Fatalf("lazy after requery: len=%d more=%v", n, g.Result().More())
	}
	s := newScreen(t, 80, 6)
	draw(s, g)
	key(g, tcell.KeyEnd)
	if g.Result().Len() <= 3 {
		t.Fatal("no lazy fetch after re-query")
	}
	if got := ids(); !reflect.DeepEqual(got, []string{"7", "5", "3", "1"}) {
		t.Fatalf("search: %v", got)
	}

	// Column filter + free WHERE + multi-key sort.
	g.AddFilter(ColumnFilter{Col: 0, Op: OpGreater, Value: "1"})
	g.SetWhere(weird + " < 7 OR city = 'Bern'")
	key(g, tcell.KeyHome) // id (End above moved to the last column)
	key(g, tcell.KeyRight)
	key(g, tcell.KeyRight) // city
	g.Action("sort")       // city ▲1 replaces name
	key(g, tcell.KeyLeft)
	key(g, tcell.KeyLeft) // id
	g.Action("sort_add")
	g.Action("sort_add") // id ▼2
	if got := ids(); !reflect.DeepEqual(got, []string{"5", "7", "3"}) {
		t.Fatalf("combined: %v\n%s", got, sqls[len(sqls)-1])
	}
	last := sqls[len(sqls)-1]
	if !strings.Contains(last, "AND ("+weird+" < 7 OR city = 'Bern') ORDER BY "+d.QuoteIdent("city")+" ASC, "+d.QuoteIdent("id")+" DESC") {
		t.Fatalf("sql %s", last)
	}

	// Column filters with special characters are bound, not inlined.
	g.RemoveAll()
	g.AddFilter(ColumnFilter{Col: 1, Op: OpStartsWith, Value: "a"})
	g.AddFilter(ColumnFilter{Col: 2, Op: OpIsNull})
	if got := ids(); len(got) != 0 {
		t.Fatalf("a… and city NULL: %v", got)
	}
	g.Remove(0)
	if got := ids(); !reflect.DeepEqual(got, []string{"4"}) {
		t.Fatalf("city NULL: %v", got)
	}
	g.RemoveAll()
	g.AddFilter(ColumnFilter{Col: 2, Op: OpEquals, Value: "x' OR '1'='1"})
	if got := ids(); len(got) != 0 {
		t.Fatalf("injection matched: %v", got)
	}

	// A rejected WHERE keeps the previous result and state.
	before := g.Result()
	g.SetWhere("no_such_column = 1")
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "no_such_column") {
		t.Fatalf("errs = %v", errs)
	}
	if g.Result() != before || g.State().Where != "" {
		t.Fatal("previous result / state not kept")
	}
	g.RemoveAll()
	if got := ids(); len(got) != 8 {
		t.Fatalf("after clear: %v", got)
	}
}
