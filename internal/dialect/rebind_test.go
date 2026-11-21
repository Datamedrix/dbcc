package dialect_test

import (
	"testing"

	"dbcc/internal/dialect"
	"dbcc/internal/dialect/mssql"
	"dbcc/internal/dialect/mysql"
	"dbcc/internal/dialect/postgres"
)

func TestRebind(t *testing.T) {
	cases := []struct {
		d       dialect.Dialect
		in, out string
	}{
		{&mysql.MySQL{}, "SELECT ? FROM t WHERE a = '?'", "SELECT ? FROM t WHERE a = '?'"},
		{&postgres.Postgres{}, `SELECT '?', "?" FROM t WHERE a = ? AND b = ? -- ?`, `SELECT '?', "?" FROM t WHERE a = $1 AND b = $2 -- ?`},
		{&mssql.MSSQL{}, "SELECT '?', [a?] FROM t WHERE a = ? AND b=? /* ? */", "SELECT '?', [a?] FROM t WHERE a = @p1 AND b=@p2 /* ? */"},
		{&mssql.MSSQL{}, "SELECT 1", "SELECT 1"},
	}
	for _, c := range cases {
		if got := dialect.Rebind(c.d, c.in); got != c.out {
			t.Errorf("%s: Rebind(%q) = %q, want %q", c.d.Name(), c.in, got, c.out)
		}
	}
}

func TestPlaceholdersAndDefaults(t *testing.T) {
	for _, c := range []struct {
		d        dialect.Dialect
		ph, defs string
	}{
		{&mysql.MySQL{}, "?", "INSERT INTO t () VALUES ()"},
		{&postgres.Postgres{}, "$3", "INSERT INTO t DEFAULT VALUES"},
		{&mssql.MSSQL{}, "@p3", "INSERT INTO t DEFAULT VALUES"},
	} {
		if got := c.d.Placeholder(3); got != c.ph && !(c.ph == "?" && got == "?") {
			t.Errorf("%s: Placeholder(3) = %q", c.d.Name(), got)
		}
		if got := c.d.InsertDefaults("t"); got != c.defs {
			t.Errorf("%s: InsertDefaults = %q", c.d.Name(), got)
		}
	}
}

func TestMultiResult(t *testing.T) {
	cases := []struct {
		d    dialect.Dialect
		stmt string
		want bool
	}{
		{&mysql.MySQL{}, "CALL p()", true},
		{&mysql.MySQL{}, "SELECT 1", false},
		{&postgres.Postgres{}, "CALL p()", false},
		{&mssql.MSSQL{}, "SELECT 1", false},
		{&mssql.MSSQL{}, "SELECT 1; SELECT 2", true},
		{&mssql.MSSQL{}, "SELECT 1 UNION ALL SELECT 2", false},
		{&mssql.MSSQL{}, "WITH x AS (SELECT 1 AS a) SELECT a FROM x", false},
		{&mssql.MSSQL{}, "IF EXISTS (SELECT 1) SELECT 2", false},
		{&mssql.MSSQL{}, "INSERT INTO t SELECT * FROM u; SELECT 1", false},
		{&mssql.MSSQL{}, "EXEC p", true},
		{&mssql.MSSQL{}, "sp_who", true},
		{&mssql.MSSQL{}, "UPDATE t SET a = 1", false},
	}
	for _, c := range cases {
		if got := c.d.MultiResult(c.stmt); got != c.want {
			t.Errorf("%s: MultiResult(%q) = %v", c.d.Name(), c.stmt, got)
		}
	}
	if !(&mssql.MSSQL{}).ReturnsRows("IF EXISTS (SELECT 1) SELECT 2") || (&mssql.MSSQL{}).ReturnsRows("INSERT INTO t SELECT 1") {
		t.Errorf("ReturnsRows changed")
	}
}

func TestPostgresBeginFor(t *testing.T) {
	p := &postgres.Postgres{}
	var _ dialect.TxnBeginner = p
	var _ dialect.TxnTracker = p
	var _ dialect.TxnTracker = &mssql.MSSQL{}
	for stmt, want := range map[string]string{
		"INSERT INTO t VALUES (1)": "BEGIN",
		"SELECT 1":                 "BEGIN",
		"COMMIT":                   "",
		"rollback":                 "",
		"BEGIN":                    "",
		"START TRANSACTION":        "",
		"SAVEPOINT a":              "",
		"SET search_path = x":      "",
		"SET TRANSACTION ISOLATION LEVEL serializable": "BEGIN",
		"SHOW search_path":                     "",
		"VACUUM t":                             "",
		"CREATE DATABASE x":                    "",
		"CREATE INDEX CONCURRENTLY i ON t (a)": "",
		"PREPARE TRANSACTION 'x'":              "",
	} {
		if got := p.BeginFor(stmt); got != want {
			t.Errorf("BeginFor(%q) = %q, want %q", stmt, got, want)
		}
	}
}
