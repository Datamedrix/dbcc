package dialect_test

import (
	"testing"

	"dbcc/internal/dialect"
	"dbcc/internal/dialect/mysql"
	"dbcc/internal/dialect/postgres"
	"dbcc/internal/sqltext"
)

func TestAppendLimit(t *testing.T) {
	tok := sqltext.NewMySQL()
	for _, tc := range []struct {
		in, out string
		ok      bool
	}{
		{"SELECT * FROM t ORDER BY id DESC", "SELECT * FROM t ORDER BY id DESC LIMIT 10", true},
		{"select * from t order by a, b desc;", "select * from t order by a, b desc LIMIT 10", true},
		{"WITH c AS (SELECT a FROM t) SELECT a FROM c ORDER BY a", "WITH c AS (SELECT a FROM t) SELECT a FROM c ORDER BY a LIMIT 10", true},
		{"SELECT * FROM t", "", false},                                       // no order: wrap as before
		{"SELECT a, ROW_NUMBER() OVER (ORDER BY a) r FROM t", "", false},     // ORDER BY only inside parentheses
		{"SELECT * FROM t ORDER BY id LIMIT 5", "", false},                   // already limited
		{"SELECT * FROM t ORDER BY id OFFSET 5", "", false},                  // PostgreSQL OFFSET
		{"SELECT * FROM t ORDER BY id FOR UPDATE", "", false},                // locking clause
		{"SELECT * FROM t ORDER BY id INTO OUTFILE '/tmp/x'", "", false},     // INTO
		{"SHOW TABLES", "", false},                                           // not a query
		{"SELECT 'ORDER BY' FROM t", "", false},                              // inside a string
		{"SELECT * FROM (SELECT * FROM t ORDER BY id LIMIT 3) x", "", false}, // inner order only
	} {
		got, ok := dialect.AppendLimit(tok, tc.in, 10)
		if got != tc.out || ok != tc.ok {
			t.Errorf("%q: %q %v, want %q %v", tc.in, got, ok, tc.out, tc.ok)
		}
	}
	if got := (&mysql.MySQL{}).LimitQuery("SELECT * FROM t ORDER BY id DESC", 3); got != "SELECT * FROM t ORDER BY id DESC LIMIT 3" {
		t.Errorf("mysql: %q", got)
	}
	if got := (&postgres.Postgres{}).LimitQuery("SELECT * FROM t", 3); got != "SELECT * FROM (SELECT * FROM t) AS dbcc_limit LIMIT 3" {
		t.Errorf("postgres: %q", got)
	}
}
