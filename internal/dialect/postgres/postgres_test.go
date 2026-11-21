package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

func TestRegistered(t *testing.T) {
	d, err := dialect.For("postgres")
	if err != nil || d.Name() != "postgres" || !d.HasSchemas() || d.LexerName() != "postgresql" {
		t.Fatalf("%v %v", d, err)
	}
}

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func TestNormalizeDefault(t *testing.T) {
	tests := []struct {
		in   sql.NullString
		want string
		has  bool
		expr bool
	}{
		{sql.NullString{}, "", false, false},
		{ns("NULL::character varying"), "", false, false},
		{ns("'EU'::bpchar"), "EU", true, false},
		{ns("'it''s'::character varying"), "it's", true, false},
		{ns("''::text"), "", true, false},
		{ns("'NULL'::text"), "NULL", true, false},
		{ns(`E'a\\b\n'::text`), "a\\b\n", true, false},
		{ns("0.00"), "0.00", true, false},
		{ns("1"), "1", true, false},
		{ns("'-1.5'::double precision"), "'-1.5'::double precision", true, true}, // see below
		{ns("(-1)"), "-1", true, false},
		{ns("true"), "true", true, false},
		{ns("'{a,b}'::text[]"), "{a,b}", true, false},
		{ns("now()"), "now()", true, true},
		{ns("CURRENT_TIMESTAMP"), "CURRENT_TIMESTAMP", true, true},
		{ns("nextval('items_id_seq'::regclass)"), "nextval('items_id_seq'::regclass)", true, true},
		{ns("('a'::text || 'b'::text)"), "('a'::text || 'b'::text)", true, true},
		{ns("'now'::text::date"), "'now'::text::date", true, true},
	}
	// A quoted negative number is a string literal with a cast: its value
	// is the text inside the quotes.
	tests[9] = struct {
		in   sql.NullString
		want string
		has  bool
		expr bool
	}{ns("'-1.5'::double precision"), "-1.5", true, false}
	for _, tt := range tests {
		v, expr := normalizeDefault(tt.in)
		got := ""
		if v != nil {
			got = *v
		}
		if got != tt.want || (v != nil) != tt.has || expr != tt.expr {
			t.Errorf("normalizeDefault(%q) = %q,%v,%v want %+v", tt.in.String, got, v != nil, expr, tt)
		}
	}
}

func TestQuoteQualifyLimit(t *testing.T) {
	p := &Postgres{}
	if got := p.QuoteIdent(`a"b`); got != `"a""b"` {
		t.Error(got)
	}
	if got := p.Qualify(meta.Path{Database: "db", Schema: "s", Object: "t"}); got != `"s"."t"` {
		t.Error(got)
	}
	if got := p.Qualify(meta.Path{Database: "db", Object: "t"}); got != `"t"` {
		t.Error(got)
	}
	if got := p.LimitQuery("SELECT 1;", 5); got != "SELECT * FROM (SELECT 1) AS dbcc_limit LIMIT 5" {
		t.Error(got)
	}
}

func TestClassification(t *testing.T) {
	p := &Postgres{}
	rows := map[string]bool{
		"SELECT 1": true, "with x as (select 1) select * from x": true, "VALUES (1)": true, "TABLE t": true,
		"SHOW search_path": true, "EXPLAIN SELECT 1": true, "FETCH 10 FROM c": true, "CALL p()": true,
		"(SELECT 1) UNION (SELECT 2)": true, "INSERT INTO t VALUES (1) RETURNING id": true,
		"DELETE FROM t RETURNING *": true, "INSERT INTO t VALUES (1)": false, "CREATE TABLE t (a int)": false,
		"DO $$ BEGIN PERFORM 1; END $$": false, "CREATE FUNCTION f() RETURNS int AS $$ SELECT 1 RETURNING $$ LANGUAGE sql": false,
		"-- c\n/* x */ select 1": true, "": false,
	}
	for s, want := range rows {
		if got := p.ReturnsRows(s); got != want {
			t.Errorf("ReturnsRows(%q) = %v", s, got)
		}
	}
	reads := map[string]bool{
		"SELECT 1": true, "SELECT * INTO t2 FROM t": false, "SELECT * FROM t FOR UPDATE": false,
		"WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d": false, "EXPLAIN ANALYZE DELETE FROM t": false,
		"EXPLAIN SELECT 1": true, "SET search_path TO x": true, "BEGIN": true, "COMMIT": true, "ROLLBACK": true,
		"SHOW ALL": true, "INSERT INTO t VALUES (1)": false, "CREATE TABLE t (a int)": false, "CALL p()": false,
		"SELECT 'delete'": true, "": true,
	}
	for s, want := range reads {
		if got := p.IsRead(s); got != want {
			t.Errorf("IsRead(%q) = %v", s, got)
		}
	}
	if p.ImplicitCommit("CREATE TABLE t (a int)") || p.ImplicitCommit("DROP TABLE t") {
		t.Error("PostgreSQL DDL is transactional")
	}
	notx := map[string]bool{
		"VACUUM t": true, "CREATE DATABASE x": true, "DROP DATABASE x": true, "CREATE INDEX CONCURRENTLY i ON t (a)": true,
		"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)": true, "CREATE INDEX i ON t (a)": false, "ALTER SYSTEM SET x = 1": true,
		"REINDEX TABLE CONCURRENTLY t": true, "REINDEX TABLE t": false, "SELECT 1": false,
	}
	for s, want := range notx {
		if got := p.NoTransactionBlock(s); got != want {
			t.Errorf("NoTransactionBlock(%q) = %v", s, got)
		}
	}
}

func cols(defs ...string) []meta.Column {
	var out []meta.Column
	for i, d := range defs {
		name, typ, _ := strings.Cut(d, ":")
		out = append(out, meta.Column{Name: name, DataType: typ, Position: i + 1})
	}
	return out
}

func TestTextSearchAndPlaceholders(t *testing.T) {
	p := &Postgres{}
	cs := cols("id:integer", "name:character varying(20)", "bin:bytea", "tags:text[]")
	got := p.TextSearchPredicate(cs, "")
	want := `(strpos(lower("id"::text), lower($1::text)) > 0 OR strpos(lower("name"), lower($1::text)) > 0 OR strpos(lower("tags"::text), lower($1::text)) > 0)`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if p.TextSearchParamCount(cs) != 1 || p.TextSearchParamCount(cols("b:bytea")) != 0 {
		t.Error("param count")
	}
	if got := p.TextSearchPredicate(cols("b:bytea"), "$3"); got != "1 = 0" {
		t.Error(got)
	}
	if got := p.TextSearchPredicate(cols("n:text"), p.Placeholder(3)); !strings.Contains(got, "lower($3::text)") {
		t.Error(got)
	}
	in := `SELECT '?', "?", $$?$$ /* ? */ FROM t WHERE a = ? AND b ?| array['x'] AND c = ? -- ?`
	if got := p.Rebind(in); got != `SELECT '?', "?", $$?$$ /* ? */ FROM t WHERE a = $1 AND b ?| array['x'] AND c = $2 -- ?` {
		t.Error(got)
	}
}

func TestCopyTableSQL(t *testing.T) {
	p := &Postgres{}
	src := meta.Path{Schema: "app", Object: "items"}
	dst := meta.Path{Schema: "app", Object: "items2"}
	got, err := p.CopyTable(src, dst, nil, false)
	if err != nil || !reflect.DeepEqual(got, []string{`CREATE TABLE "app"."items2" AS SELECT * FROM "app"."items"`}) {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := p.CopyTable(src, dst, nil, true); err == nil {
		t.Error("structure copy without metadata")
	}
	def := "nextval('app.items_seq_no_seq'::regclass)"
	tbl := &meta.Table{Kind: meta.KindTable, Options: map[string]string{"identity.id": "ALWAYS"}, Columns: []meta.Column{
		{Name: "id", DataType: "bigint", AutoIncrement: true},
		{Name: "seq_no", DataType: "integer", AutoIncrement: true, Default: &def, DefaultExpr: true},
		{Name: "name", DataType: "text"},
		{Name: "total", DataType: "numeric", Generated: &meta.Generated{Expression: "a*b", Stored: true}},
	}}
	got, err = p.CopyTable(src, dst, tbl, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`CREATE TABLE "app"."items2" (LIKE "app"."items" INCLUDING ALL)`,
		`CREATE SEQUENCE "app"."items2_seq_no_seq" AS integer OWNED BY "app"."items2"."seq_no"`,
		`ALTER TABLE "app"."items2" ALTER COLUMN "seq_no" SET DEFAULT nextval('"app"."items2_seq_no_seq"'::regclass)`,
		`INSERT INTO "app"."items2" ("id", "seq_no", "name") OVERRIDING SYSTEM VALUE SELECT "id", "seq_no", "name" FROM "app"."items"`,
		`SELECT setval(pg_get_serial_sequence('"app"."items2"', 'id'), max("id")) FROM "app"."items2"`,
		`SELECT setval(pg_get_serial_sequence('"app"."items2"', 'seq_no'), max("seq_no")) FROM "app"."items2"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	view := &meta.Table{Kind: meta.KindView, ViewDefinition: " SELECT 1 AS a", Options: map[string]string{"options": "check_option=local"}}
	got, _ = p.CopyTable(src, dst, view, true)
	if len(got) != 1 || got[0] != "CREATE VIEW \"app\".\"items2\" WITH (check_option=local) AS\n SELECT 1 AS a" {
		t.Errorf("%q", got)
	}
	if n := truncIdent(strings.Repeat("ä", 40)); len(n) > 63 || !strings.HasPrefix(n, "ää") || len(n)%2 != 0 {
		t.Errorf("truncIdent %q", n)
	}
}

func pgErr(code, msg, detail, col, cons string) error {
	return fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code, Message: msg, Detail: detail, ColumnName: col, ConstraintName: cons})
}

func TestMapErrorMessages(t *testing.T) {
	p := &Postgres{}
	tbl := &meta.Table{
		Path: meta.Path{Schema: "public", Object: "items"},
		Columns: []meta.Column{
			{Name: "id", DataType: "integer"}, {Name: "code", DataType: "character varying(20)"},
			{Name: "note", DataType: "character varying(40)"}, {Name: "qty", DataType: "integer"},
			{Name: "price", DataType: "numeric(10,2)"}, {Name: "created", DataType: "date"},
			{Name: "Mixed", DataType: "text"}, {Name: "small", DataType: "smallint"},
		},
		UniqueKeys: []meta.Key{{Name: "uq_code", Columns: []string{"code"}}},
		Checks:     []meta.Check{{Name: "chk_qty", Expression: "qty > 0 AND qty < 1000"}, {Name: "chk_two", Expression: "qty < id"}, {Name: "chk_mixed", Expression: `length("Mixed") > 1`}},
	}
	tests := []struct {
		err       error
		col, cons string
	}{
		{pgErr("23502", `null value in column "code" violates not-null constraint`, "", "code", ""), "code", ""},
		{pgErr("23505", `duplicate key value violates unique constraint "uq_code"`, "Key (code)=(x) already exists.", "", "uq_code"), "code", "uq_code"},
		{pgErr("23505", `duplicate key`, `Key ("Mixed", code)=(x) already exists.`, "", "uq_x"), "Mixed", "uq_x"},
		{pgErr("23505", `duplicate key`, "", "", "uq_code"), "code", "uq_code"},
		{pgErr("23503", `insert or update on table "items" violates foreign key constraint "fk"`, `Key (parent_a, parent_b)=(9, z) is not present in table "parent".`, "", "fk"), "parent_a", "fk"},
		{pgErr("23514", `new row for relation "items" violates check constraint "chk_qty"`, "", "", "chk_qty"), "qty", "chk_qty"},
		{pgErr("23514", `check`, "", "", "chk_two"), "", "chk_two"},
		{pgErr("23514", `check`, "", "", "chk_mixed"), "Mixed", "chk_mixed"},
		{pgErr("22001", "value too long for type character varying(20)", "", "", ""), "code", ""},
		{pgErr("22003", "smallint out of range", "", "", ""), "small", ""},
		{pgErr("22003", "integer out of range", "", "", ""), "", ""}, // ambiguous: id, qty
		{pgErr("22003", "numeric field overflow", "A field with precision 10, scale 2 must round to an absolute value less than 10^8.", "", ""), "price", ""},
		{pgErr("22P02", `invalid input syntax for type numeric: "abc"`, "", "", ""), "price", ""},
		{pgErr("22007", `invalid input syntax for type date: "x"`, "", "", ""), "created", ""},
		{pgErr("22008", `date/time field value out of range: "2024-13-01"`, "", "", ""), "created", ""},
		{pgErr("428C9", `cannot insert a non-DEFAULT value into column "id"`, `Column "id" is an identity column defined as GENERATED ALWAYS.`, "", ""), "id", ""},
	}
	for _, tt := range tests {
		fe := p.MapError(tt.err, tbl)
		if fe == nil || fe.Column != tt.col || fe.Constraint != tt.cons {
			t.Errorf("%v: %+v, want col %q cons %q", tt.err, fe, tt.col, tt.cons)
			continue
		}
		if !errors.Is(fe, tt.err) && fe.Unwrap() != tt.err {
			t.Errorf("unwrap %v", fe)
		}
	}
	if fe := p.MapError(pgErr("42P01", `relation "x" does not exist`, "", "", ""), tbl); fe != nil {
		t.Errorf("unrelated: %+v", fe)
	}
	if fe := p.MapError(errors.New("x"), tbl); fe != nil {
		t.Error("non-pg error mapped")
	}
	if fe := p.MapError(pgErr("23505", "dup", "Key (code)=(x) already exists.", "", "uq"), nil); fe == nil || fe.Column != "code" {
		t.Errorf("nil table: %+v", fe)
	}
}

func TestTriggerEvent(t *testing.T) {
	if got := triggerEvent(tgRow|tgInsert|tgUpdate, "a, b", "(new.a > 0)", "O"); got != "INSERT OR UPDATE OF a, b FOR EACH ROW WHEN (new.a > 0)" {
		t.Error(got)
	}
	if got := triggerEvent(tgTruncate, "", "", "D"); got != "TRUNCATE FOR EACH STATEMENT (disabled)" {
		t.Error(got)
	}
	if triggerTiming(tgBefore) != "BEFORE" || triggerTiming(tgInstead|tgRow) != "INSTEAD OF" || triggerTiming(0) != "AFTER" {
		t.Error("timing")
	}
}

func TestConnConfig(t *testing.T) {
	p := &Postgres{}
	prof := &config.Profile{Params: map[string]string{"sslmode": "disable", "application_name": "x y'z"}}
	cfg, err := p.connConfig(prof, config.Credentials{Socket: "/run/postgresql/.s.PGSQL.5433", User: "u", Password: `p'w\`}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "/run/postgresql" || cfg.Port != 5433 || cfg.User != "u" || cfg.Password != `p'w\` || cfg.Database != "postgres" ||
		cfg.RuntimeParams["application_name"] != "x y'z" || cfg.TLSConfig != nil || cfg.DefaultQueryExecMode != pgx.QueryExecModeDescribeExec {
		t.Errorf("%+v", cfg.Config)
	}
	cfg, err = p.connConfig(nil, config.Credentials{Host: "db.local", Port: 6000}, "shop")
	if err != nil || cfg.Host != "db.local" || cfg.Port != 6000 || cfg.Database != "shop" {
		t.Errorf("%+v %v", cfg, err)
	}
}

func TestKeywordsFunctions(t *testing.T) {
	p := &Postgres{}
	for _, l := range [][]string{p.Keywords(), p.Functions()} {
		if len(l) < 100 {
			t.Errorf("only %d entries", len(l))
		}
		for i := 1; i < len(l); i++ {
			if l[i-1] >= l[i] {
				t.Errorf("not sorted/unique at %q", l[i])
			}
		}
	}
	_ = sqltext.NewPostgres()
}
