package mssql

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	driver "github.com/microsoft/go-mssqldb"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
)

func TestRegistered(t *testing.T) {
	d, err := dialect.For("mssql")
	if err != nil || d.Name() != "mssql" || !d.HasSchemas() || d.LexerName() != "tsql" {
		t.Fatalf("dialect.For(mssql) = %v, %v", d, err)
	}
	if !errors.Is(d.Cancel(context.Background(), nil, "1"), dialect.ErrCancelViaContext) {
		t.Error("Cancel must cancel via context")
	}
	if d.ImplicitCommit("CREATE TABLE t (a int)") {
		t.Error("DDL does not commit implicitly on MSSQL")
	}
	if len(d.Keywords()) < 100 || len(d.Functions()) < 100 {
		t.Error("keyword or function list too short")
	}
}

func TestQuoteQualify(t *testing.T) {
	m := &MSSQL{}
	if got := m.QuoteIdent("a]b"); got != "[a]]b]" {
		t.Errorf("QuoteIdent = %s", got)
	}
	for _, tc := range []struct {
		p    meta.Path
		want string
	}{
		{meta.Path{Database: "d", Schema: "s", Object: "o"}, "[d].[s].[o]"},
		{meta.Path{Database: "d", Object: "o"}, "[d]..[o]"},
		{meta.Path{Schema: "s", Object: "o"}, "[s].[o]"},
		{meta.Path{Object: "o"}, "[o]"},
	} {
		if got := m.Qualify(tc.p); got != tc.want {
			t.Errorf("Qualify(%+v) = %s, want %s", tc.p, got, tc.want)
		}
	}
}

func TestLimitQueryText(t *testing.T) {
	m := &MSSQL{}
	for _, tc := range []struct{ in, want string }{
		{"SELECT * FROM t;", "SELECT TOP (10) * FROM t"},
		{"select distinct a from t order by a", "select distinct TOP (10) a from t order by a"},
		{"WITH c AS (SELECT a FROM t) SELECT a FROM c ORDER BY a", "WITH c AS (SELECT a FROM t) SELECT TOP (10) a FROM c ORDER BY a"},
		{"SELECT TOP 5 a FROM t ORDER BY a", "SELECT TOP (10) * FROM (SELECT TOP 5 a FROM t ORDER BY a) AS dbcc_limit"},
		{"SELECT a FROM t UNION SELECT b FROM u ORDER BY a", "SELECT TOP (10) * FROM (SELECT a FROM t UNION SELECT b FROM u) AS dbcc_limit ORDER BY a"},
		{"SELECT a FROM t ORDER BY a OFFSET 5 ROWS", "SELECT TOP (10) * FROM (SELECT a FROM t ORDER BY a OFFSET 5 ROWS) AS dbcc_limit"},
		{"SELECT a, ROW_NUMBER() OVER (ORDER BY a) r FROM t", "SELECT TOP (10) a, ROW_NUMBER() OVER (ORDER BY a) r FROM t"},
		{"SELECT a FROM (SELECT TOP 3 a FROM t) x", "SELECT TOP (10) a FROM (SELECT TOP 3 a FROM t) x"},
	} {
		if got := m.LimitQuery(tc.in, 10); got != tc.want {
			t.Errorf("LimitQuery(%q)\n got %s\nwant %s", tc.in, got, tc.want)
		}
	}
}

func TestReturnsRows(t *testing.T) {
	m := &MSSQL{}
	for _, tc := range []struct {
		stmt string
		want bool
	}{
		{"SELECT 1", true},
		{"-- c\nselect * from t", true},
		{"(SELECT 1) UNION (SELECT 2)", true},
		{"WITH c AS (SELECT 1 a) SELECT * FROM c", true},
		{"WITH c AS (SELECT 1 a) DELETE FROM t WHERE a IN (SELECT a FROM c)", false},
		{"INSERT INTO t (a) VALUES (1)", false},
		{"INSERT INTO t (a) SELECT a FROM u WHERE b IN (SELECT b FROM v)", false},
		{"INSERT INTO t SELECT a FROM u UNION ALL SELECT a FROM w", false},
		{"INSERT INTO t EXEC p", false},
		{"INSERT INTO t VALUES (1); SELECT * FROM t", true},
		{"INSERT INTO t VALUES (1) SELECT * FROM t", true},
		{"INSERT INTO t SELECT 1 SELECT 2", true},
		{"INSERT INTO t OUTPUT inserted.id VALUES (1)", true},
		{"UPDATE t SET a = (SELECT MAX(b) FROM u)", false},
		{"DELETE FROM t WHERE id IN (SELECT id FROM u)", false},
		{"IF EXISTS (SELECT 1 FROM t) UPDATE t SET a = 1", false},
		{"IF EXISTS (SELECT 1 FROM t) SELECT 1", true},
		{"EXEC sales.calc_total", true},
		{"sp_who2", true},
		{"DBCC CHECKIDENT ('t', NORESEED)", true},
		{"CREATE TABLE t (a int)", false},
		{"CREATE PROCEDURE p AS SELECT 1", true}, // harmless: Query on DDL
		{"SET NOCOUNT ON", false},
		{"DECLARE @x int = 1; SET @x = 2", false},
		{"WAITFOR DELAY '00:00:01'", false},
		{"PRINT 'SELECT'", false},
		{"", false},
	} {
		if got := m.ReturnsRows(tc.stmt); got != tc.want {
			t.Errorf("ReturnsRows(%q) = %v, want %v", tc.stmt, got, tc.want)
		}
	}
}

func TestIsRead(t *testing.T) {
	m := &MSSQL{}
	for _, tc := range []struct {
		stmt string
		want bool
	}{
		{"SELECT * FROM t", true},
		{"WITH c AS (SELECT 1 a) SELECT * FROM c", true},
		{"SELECT a INTO #t FROM u", false},
		{"SET NOCOUNT ON", true},
		{"USE master", true},
		{"DECLARE @x int; SELECT @x = COUNT(*) FROM t; PRINT @x", true},
		{"IF EXISTS (SELECT 1 FROM t) SELECT 1 ELSE SELECT 2", true},
		{"IF 1 = 1 DELETE FROM t", false},
		{"BEGIN TRAN", true},
		{"COMMIT", true},
		{"SELECT 1; UPDATE t SET a = 1", false},
		{"EXEC p", false},
		{"sp_who", false},
		{"INSERT INTO t VALUES (1)", false},
		{"MERGE t USING u ON 1 = 0 WHEN NOT MATCHED THEN INSERT VALUES (1);", false},
		{"TRUNCATE TABLE t", false},
		{"SELECT 'DELETE' AS [update]", true},
		{"", true},
	} {
		if got := m.IsRead(tc.stmt); got != tc.want {
			t.Errorf("IsRead(%q) = %v, want %v", tc.stmt, got, tc.want)
		}
	}
}

func TestParseDefault(t *testing.T) {
	for _, tc := range []struct {
		def, want string
		expr      bool
	}{
		{"((0))", "0", false},
		{"((-1.5))", "-1.5", false},
		{"(N'it''s')", "it's", false},
		{"('a')", "a", false},
		{"(getdate())", "getdate()", true},
		{"((1)+(2))", "(1)+(2)", true},
		{"(NEXT VALUE FOR [s].[q])", "NEXT VALUE FOR [s].[q]", true},
		{"('a')+('b')", "('a')+('b')", true},
		{"(NULL)", "<nil>", false},
	} {
		v, expr := parseDefault(tc.def)
		got := "<nil>"
		if v != nil {
			got = *v
		}
		if got != tc.want || expr != tc.expr {
			t.Errorf("parseDefault(%q) = %q %v, want %q %v", tc.def, got, expr, tc.want, tc.expr)
		}
	}
}

func TestTypeDecl(t *testing.T) {
	for _, tc := range []struct {
		name                string
		maxLen, prec, scale int
		want                string
	}{
		{"nvarchar", 80, 0, 0, "nvarchar(40)"},
		{"nvarchar", -1, 0, 0, "nvarchar(max)"},
		{"varbinary", -1, 0, 0, "varbinary(max)"},
		{"char", 10, 0, 0, "char(10)"},
		{"decimal", 9, 10, 2, "decimal(10,2)"},
		{"datetime2", 8, 27, 7, "datetime2(7)"},
		{"float", 8, 53, 0, "float"},
		{"float", 4, 24, 0, "float(24)"},
		{"int", 4, 10, 0, "int"},
	} {
		if got := typeDecl(tc.name, tc.maxLen, tc.prec, tc.scale); got != tc.want {
			t.Errorf("typeDecl(%s, %d, %d, %d) = %s, want %s", tc.name, tc.maxLen, tc.prec, tc.scale, got, tc.want)
		}
	}
}

func TestRewriteCreate(t *testing.T) {
	m := &MSSQL{}
	obj := meta.Path{Database: "d", Schema: "s", Object: "p"}
	for _, tc := range []struct {
		src, want string
		orAlter   bool
	}{
		{"-- head\nCREATE PROC p AS SELECT 1", "-- head\nCREATE OR ALTER PROC [s].[p] AS SELECT 1", true},
		{"/* c */ create procedure dbo.old_name\n@a int AS SELECT @a", "/* c */ create OR ALTER procedure [s].[p]\n@a int AS SELECT @a", true},
		{"CREATE OR ALTER VIEW [s].[p] AS SELECT 1 a", "CREATE OR ALTER VIEW [s].[p] AS SELECT 1 a", true},
		{"CREATE TRIGGER p ON items AFTER INSERT AS SELECT 1", "CREATE OR ALTER TRIGGER [s].[p] ON [s].items AFTER INSERT AS SELECT 1", true},
		{"CREATE TRIGGER s.p ON s.items AFTER INSERT AS SELECT 1", "CREATE OR ALTER TRIGGER [s].[p] ON s.items AFTER INSERT AS SELECT 1", true},
		{"CREATE FUNCTION p() RETURNS int AS BEGIN RETURN 1 END", "ALTER FUNCTION [s].[p]() RETURNS int AS BEGIN RETURN 1 END", false},
		{"CREATE OR ALTER VIEW p AS SELECT 1 a", "ALTER VIEW [s].[p] AS SELECT 1 a", false},
	} {
		got, _, err := m.rewriteCreate(tc.src, obj, tc.orAlter)
		if err != nil || got != tc.want {
			t.Errorf("rewriteCreate(%q)\n got %q (%v)\nwant %q", tc.src, got, err, tc.want)
		}
	}
	if _, _, err := m.rewriteCreate("ALTER VIEW v AS SELECT 1", obj, true); err == nil {
		t.Error("source without CREATE accepted")
	}
}

func TestCopyTableDDL(t *testing.T) {
	m := &MSSQL{}
	def := func(s string) *string { return &s }
	tbl := &meta.Table{
		Kind: meta.KindTable,
		Columns: []meta.Column{
			{Name: "id", DataType: "int", AutoIncrement: true},
			{Name: "code", DataType: "char(3)", Collation: "Latin1_General_CS_AS", Default: def("007")},
			{Name: "n", DataType: "nvarchar(10)", Nullable: true, Default: def("it's")},
			{Name: "tot", DataType: "int", Nullable: true, Generated: &meta.Generated{Expression: "([id]*(2))", Stored: true}},
			{Name: "v", DataType: "timestamp"},
			{Name: "at", DataType: "datetime2(3)", Default: def("sysutcdatetime()"), DefaultExpr: true},
		},
		PrimaryKey: &meta.Key{Name: "PK_t", Columns: []string{"id"}},
		Checks:     []meta.Check{{Name: "CK_t_code", Expression: "([code]<>'')"}, {Name: "ck_off", Expression: "[id] > 0", NotEnforced: true}},
		Indexes: []meta.Index{
			{Name: "PK_t", Type: "CLUSTERED", Unique: true, Columns: []meta.IndexColumn{{Name: "id"}}},
			{Name: "ix_n", Type: "NONCLUSTERED", Columns: []meta.IndexColumn{{Name: "n", Descending: true}},
				Include: []string{"code"}, Filter: "([n] IS NOT NULL)"},
			{Name: "xml_ix", Type: "XML", Columns: []meta.IndexColumn{{Name: "n"}}},
		},
		Options: map[string]string{"identity": "IDENTITY(10,2)"},
	}
	got, err := m.CopyTable(meta.Path{Database: "d", Schema: "s", Object: "t"}, meta.Path{Database: "d", Object: "t2"}, tbl, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"CREATE TABLE [d].[s].[t2] (\n" +
			"    [id] int IDENTITY(10,2) NOT NULL,\n" +
			"    [code] char(3) COLLATE Latin1_General_CS_AS NOT NULL DEFAULT ('007'),\n" +
			"    [n] nvarchar(10) NULL DEFAULT (N'it''s'),\n" +
			"    [tot] AS ([id]*(2)) PERSISTED,\n" +
			"    [v] timestamp NOT NULL,\n" +
			"    [at] datetime2(3) NOT NULL DEFAULT (sysutcdatetime()),\n" +
			"    CONSTRAINT [PK_t2] PRIMARY KEY CLUSTERED ([id] ASC),\n" +
			"    CONSTRAINT [CK_t2_code] CHECK ([code]<>'')\n)",
		"SET IDENTITY_INSERT [d].[s].[t2] ON;\nINSERT INTO [d].[s].[t2] ([id], [code], [n], [at])\nSELECT [id], [code], [n], [at] FROM [d].[s].[t];\nSET IDENTITY_INSERT [d].[s].[t2] OFF;",
		"CREATE NONCLUSTERED INDEX [ix_n] ON [d].[s].[t2] ([n] DESC) INCLUDE ([code]) WHERE ([n] IS NOT NULL)",
		"ALTER TABLE [d].[s].[t2] WITH NOCHECK ADD CONSTRAINT [ck_off_t2] CHECK ([id] > 0)",
		"ALTER TABLE [d].[s].[t2] NOCHECK CONSTRAINT [ck_off_t2]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CopyTable:\n%s\n--- want\n%s", strings.Join(got, "\n;\n"), strings.Join(want, "\n;\n"))
	}
	data, _ := m.CopyTable(meta.Path{Schema: "s", Object: "t"}, meta.Path{Object: "t2"}, nil, false)
	if !reflect.DeepEqual(data, []string{"SELECT * INTO [s].[t2] FROM [s].[t]"}) {
		t.Errorf("data-only copy = %v", data)
	}
	view, _ := m.CopyTable(meta.Path{Database: "d", Schema: "s", Object: "v"}, meta.Path{Database: "d", Schema: "s", Object: "v2"},
		&meta.Table{Kind: meta.KindView, ViewDefinition: "SELECT 'x' AS a"}, true)
	if !reflect.DeepEqual(view, []string{"EXEC [d].sys.sp_executesql N'CREATE VIEW [s].[v2] AS SELECT ''x'' AS a'"}) {
		t.Errorf("view copy = %v", view)
	}
}

func TestTextSearchPredicate(t *testing.T) {
	m := &MSSQL{}
	cols := []meta.Column{{Name: "a", DataType: "nvarchar(10)"}, {Name: "b", DataType: "int"}, {Name: "c", DataType: "varbinary(8)"}}
	want := "(CHARINDEX(LOWER(@p3), LOWER([a])) > 0 OR CHARINDEX(LOWER(@p3), LOWER(CAST([b] AS NVARCHAR(MAX)))) > 0)"
	if got := m.TextSearchPredicate(cols, "@p3"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := m.TextSearchPredicate(cols[2:], ""); got != "1 = 0" {
		t.Errorf("no searchable columns: %s", got)
	}
	if got := m.TextSearchPredicate(cols[:1], ""); !strings.Contains(got, "@p1") {
		t.Errorf("default placeholder: %s", got)
	}
}

func srvErr(num int32, msg string) error {
	e := driver.Error{Number: num, Message: msg}
	e.All = []driver.Error{e, {Number: 3621, Message: "The statement has been terminated."}}
	// As go-mssqldb reports it: the last message describes the error.
	last := e.All[1]
	last.All = e.All
	return fmt.Errorf("wrapped: %w", last)
}

func TestMapErrorMessages(t *testing.T) {
	m := &MSSQL{}
	tbl := &meta.Table{
		Path:        meta.Path{Schema: "s", Object: "t"},
		Columns:     []meta.Column{{Name: "id", AutoIncrement: true}, {Name: "qty"}, {Name: "p_a"}, {Name: "it's"}},
		PrimaryKey:  &meta.Key{Name: "PK_t", Columns: []string{"id"}},
		UniqueKeys:  []meta.Key{{Name: "UX_t", Columns: []string{"qty", "p_a"}}},
		ForeignKeys: []meta.ForeignKey{{Name: "FK_t_p", Columns: []string{"p_a"}, RefColumns: []string{"a"}}},
		Checks:      []meta.Check{{Name: "CK_t", Expression: "([qty]>(0) AND [qty]<(9))"}},
	}
	for _, tc := range []struct {
		err       error
		col, cons string
	}{
		{srvErr(515, "Cannot insert the value NULL into column 'it''s', table 'd.s.t'; column does not allow nulls. INSERT fails."), "it's", ""},
		{srvErr(2627, "Violation of PRIMARY KEY constraint 'PK_t'. Cannot insert duplicate key in object 's.t'. The duplicate key value is (1)."), "id", "PK_t"},
		{srvErr(2601, "Cannot insert duplicate key row in object 's.t' with unique index 'UX_t'. The duplicate key value is (1, 2)."), "qty", "UX_t"},
		{srvErr(547, `The INSERT statement conflicted with the FOREIGN KEY constraint "FK_t_p". The conflict occurred in database "d", table "s.p", column 'a'.`), "p_a", "FK_t_p"},
		{srvErr(547, `The INSERT statement conflicted with the CHECK constraint "CK_t". The conflict occurred in database "d", table "s.t".`), "qty", "CK_t"},
		{srvErr(547, `The INSERT statement conflicted with the CHECK constraint "CK_t". The conflict occurred in database "d", table "s.t", column 'qty'.`), "qty", "CK_t"},
		{srvErr(547, `The DELETE statement conflicted with the REFERENCE constraint "FK_c". The conflict occurred in database "d", table "s.c", column 'x'.`), "", "FK_c"},
		{srvErr(2628, "String or binary data would be truncated in table 'd.s.t', column 'qty'. Truncated value: 'ab'."), "qty", ""},
		{srvErr(8152, "String or binary data would be truncated."), "", ""},
		{srvErr(544, "Cannot insert explicit value for identity column in table 't' when IDENTITY_INSERT is set to OFF."), "id", ""},
		{srvErr(271, `The column "qty" cannot be modified because it is either a computed column or is the result of a UNION operator.`), "qty", ""},
	} {
		fe := m.MapError(tc.err, tbl)
		if fe == nil {
			t.Errorf("%v: not mapped", tc.err)
			continue
		}
		if fe.Column != tc.col || fe.Constraint != tc.cons {
			t.Errorf("%s: column %q constraint %q, want %q %q", fe.Message, fe.Column, fe.Constraint, tc.col, tc.cons)
		}
		if !errors.Is(fe, fe.Err) || fe.Err != tc.err {
			t.Errorf("%s: Err not kept", fe.Message)
		}
	}
	if fe := m.MapError(srvErr(208, "Invalid object name 'x'."), tbl); fe != nil {
		t.Errorf("unrelated error mapped: %+v", fe)
	}
	if fe := m.MapError(errors.New("x"), tbl); fe != nil {
		t.Errorf("non-server error mapped: %+v", fe)
	}
	if fe := m.MapError(srvErr(515, "Cannot insert the value NULL into column 'qty', table 'd.s.t'"), nil); fe == nil || fe.Column != "qty" {
		t.Errorf("nil table: %+v", fe)
	}
}
