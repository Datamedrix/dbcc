package mssql

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

// code returns the code tokens of src (no whitespace or comments).
func code(src string) []sqltext.Token {
	var out []sqltext.Token
	for _, t := range sqltext.NewMSSQL().Tokenize(src) {
		if t.Kind != sqltext.TokWhitespace && t.Kind != sqltext.TokComment {
			out = append(out, t)
		}
	}
	return out
}

func isWord(t sqltext.Token, w string) bool {
	return t.Kind == sqltext.TokIdent && strings.EqualFold(t.Text, w)
}

// LimitQuery limits a query to n rows. T-SQL has no LIMIT and a derived
// table must not contain ORDER BY (without TOP/OFFSET), needs named and
// unique columns and cannot hold a CTE, so wrapping is the last resort:
//
//  1. A SELECT (or WITH … SELECT) without TOP, OFFSET or a top-level
//     UNION/EXCEPT/INTERSECT gets TOP (n) injected after SELECT [ALL |
//     DISTINCT]; its ORDER BY is kept and applies.
//  2. Anything else is wrapped: SELECT TOP (n) * FROM (q) AS dbcc_limit.
//     A top-level ORDER BY without TOP or OFFSET (a UNION's ORDER BY) is
//     moved to the outer query, where it refers to the output columns.
//
// EXEC and other statements that are not queries cannot be limited; the
// wrapped text then fails on the server.
func (m *MSSQL) LimitQuery(query string, n int) string {
	q := strings.TrimSpace(query)
	for strings.HasSuffix(q, ";") {
		q = strings.TrimSpace(strings.TrimSuffix(q, ";"))
	}
	toks := code(q)
	top := fmt.Sprintf("TOP (%d)", n)
	depth := 0
	mainSelect, orderAt := -1, -1
	hasTop, hasSetOp, hasOffset := false, false, false
	withCTE := len(toks) > 0 && isWord(toks[0], "WITH")
	for i, t := range toks {
		switch {
		case t.Text == "(":
			depth++
		case t.Text == ")":
			depth--
		case depth != 0:
		case isWord(t, "SELECT") && mainSelect < 0 && (i == 0 || withCTE):
			mainSelect = i
		case isWord(t, "TOP") && mainSelect >= 0 && i <= mainSelect+2:
			hasTop = true
		case isWord(t, "UNION") || isWord(t, "EXCEPT") || isWord(t, "INTERSECT"):
			hasSetOp = true
		case isWord(t, "OFFSET"):
			hasOffset = true
		case isWord(t, "ORDER") && i+1 < len(toks) && isWord(toks[i+1], "BY"):
			orderAt = i
		}
	}
	if mainSelect >= 0 && !hasTop && !hasSetOp && !hasOffset {
		at := toks[mainSelect].End
		if k := mainSelect + 1; k < len(toks) && (isWord(toks[k], "DISTINCT") || isWord(toks[k], "ALL")) {
			at = toks[k].End
		}
		return q[:at] + " " + top + q[at:]
	}
	order := ""
	if orderAt >= 0 && !hasOffset && !hasTop {
		order = " " + q[toks[orderAt].Start:]
		q = strings.TrimSpace(q[:toks[orderAt].Start])
	}
	return "SELECT " + top + " * FROM (" + q + ") AS dbcc_limit" + order
}

// isStringType reports types whose default literals are quoted.
func isStringType(dataType string) bool {
	switch baseType(dataType) {
	case "char", "varchar", "nchar", "nvarchar", "text", "ntext", "date", "time", "datetime",
		"datetime2", "smalldatetime", "datetimeoffset", "uniqueidentifier", "xml", "sysname":
		return true
	}
	return false
}

func isUnicodeType(dataType string) bool {
	switch baseType(dataType) {
	case "nchar", "nvarchar", "ntext", "sysname", "xml":
		return true
	}
	return false
}

func baseType(dataType string) string {
	t := strings.ToLower(strings.TrimSpace(dataType))
	if i := strings.IndexAny(t, "( "); i >= 0 {
		t = t[:i]
	}
	return t
}

// notInsertable reports columns that cannot receive values in INSERT.
func notInsertable(c meta.Column) bool {
	if c.Generated != nil && !isPeriodColumn(c) {
		return true
	}
	switch baseType(c.DataType) {
	case "timestamp", "rowversion":
		return true
	}
	return false
}

func isPeriodColumn(c meta.Column) bool {
	return c.Generated != nil && strings.HasPrefix(c.Generated.Expression, "GENERATED ALWAYS")
}

// NString quotes s as an N'…' literal.
func NString(s string) string { return "N'" + strings.ReplaceAll(s, "'", "''") + "'" }

func literal(c meta.Column) string {
	v := *c.Default
	if c.DefaultExpr {
		return v
	}
	if !isStringType(c.DataType) && numericLiteral.MatchString(v) {
		return v
	}
	if isUnicodeType(c.DataType) {
		return NString(v)
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// renamed derives a constraint name for the copy: the source table name in
// it is replaced by the target name, else the target name is appended.
// Constraint names are unique per schema, so they cannot be reused.
func renamed(name, src, dst string) string {
	if i := strings.Index(strings.ToLower(name), strings.ToLower(src)); i >= 0 && src != "" {
		return name[:i] + dst + name[i+len(src):]
	}
	return name + "_" + dst
}

var reIdentity = regexp.MustCompile(`^IDENTITY\(([^,]+),([^)]+)\)$`)

func (m *MSSQL) indexColumns(cols []meta.IndexColumn) string {
	var parts []string
	for _, c := range cols {
		dir := " ASC"
		if c.Descending {
			dir = " DESC"
		}
		parts = append(parts, m.QuoteIdent(c.Name)+dir)
	}
	return strings.Join(parts, ", ")
}

func (t indexes) find(name string) *meta.Index {
	for i := range t {
		if t[i].Name == name {
			return &t[i]
		}
	}
	return nil
}

type indexes []meta.Index

// CopyTable returns the statements of cp (spec 8.2).
//
//   - Data only: SELECT * INTO dst FROM src (a view becomes a snapshot
//     table, A2; identity is carried over by SELECT INTO).
//   - With structure, view: a second view with the same definition (A2),
//     created through [db].sys.sp_executesql because CREATE VIEW takes no
//     database name.
//   - With structure, table: CREATE TABLE generated from t (spec 4: the
//     one place with DDL reconstruction): columns with declared type,
//     collation, IDENTITY(seed,increment), NULL/NOT NULL and defaults;
//     computed columns (PERSISTED [NOT NULL]); the primary key (clustered
//     or not, column order) and unique constraints under derived names
//     (renamed); enabled check constraints. Then one batch
//     SET IDENTITY_INSERT ON; INSERT … SELECT; SET IDENTITY_INSERT OFF
//     without computed and rowversion columns (period columns of a
//     system-versioned table become ordinary datetime2 columns and are
//     copied). Then the remaining indexes with their names, uniqueness,
//     clustering, column order, INCLUDE columns, filter and fill factor;
//     clustered and nonclustered columnstore indexes. Disabled check
//     constraints are added afterwards WITH NOCHECK and disabled.
//   - Not copied: foreign keys (A1), triggers, default and other
//     constraint names, XML, spatial and hash indexes, filegroups and
//     partitioning, compression, system versioning, extended properties.
func (m *MSSQL) CopyTable(src, dst meta.Path, t *meta.Table, withStructure bool) ([]string, error) {
	if src.Object == "" || dst.Object == "" {
		return nil, fmt.Errorf("copy needs a source and a target name")
	}
	if dst.Schema == "" {
		dst.Schema = src.Schema
	}
	if !withStructure {
		return []string{"SELECT * INTO " + m.Qualify(dst) + " FROM " + m.Qualify(src)}, nil
	}
	if t == nil {
		return nil, fmt.Errorf("copy with structure needs the metadata of %s", m.Qualify(src))
	}
	if t.Kind == meta.KindView {
		if t.ViewDefinition == "" {
			return nil, fmt.Errorf("no view definition for %s", m.Qualify(src))
		}
		body := "CREATE VIEW " + m.Qualify(meta.Path{Schema: dst.Schema, Object: dst.Object}) + " AS " + t.ViewDefinition
		if dst.Database == "" {
			return []string{body}, nil
		}
		return []string{"EXEC " + m.QuoteIdent(dst.Database) + ".sys.sp_executesql " + NString(body)}, nil
	}
	target := m.Qualify(dst)
	var defs, insertCols []string
	identity := false
	for _, c := range t.Columns {
		name := m.QuoteIdent(c.Name)
		if c.Generated != nil && !isPeriodColumn(c) {
			d := name + " AS " + c.Generated.Expression
			if c.Generated.Stored {
				d += " PERSISTED"
				if !c.Nullable {
					d += " NOT NULL"
				}
			}
			defs = append(defs, d)
			continue
		}
		d := name + " " + c.DataType
		if c.Collation != "" && !strings.HasPrefix(c.DataType, "[") { // not for alias types
			d += " COLLATE " + c.Collation
		}
		if c.AutoIncrement {
			identity = true
			seed, inc := "1", "1"
			if sm := reIdentity.FindStringSubmatch(t.Options[optIdentity]); sm != nil {
				seed, inc = sm[1], sm[2]
			}
			d += " IDENTITY(" + seed + "," + inc + ")"
		}
		if c.Nullable {
			d += " NULL"
		} else {
			d += " NOT NULL"
		}
		if c.Default != nil {
			d += " DEFAULT (" + literal(c) + ")"
		}
		defs = append(defs, d)
		if !notInsertable(c) {
			insertCols = append(insertCols, name)
		}
	}
	if len(insertCols) == 0 {
		return nil, fmt.Errorf("%s has no insertable columns", m.Qualify(src))
	}
	ixs := indexes(t.Indexes)
	constraintIx := map[string]bool{}
	if pk := t.PrimaryKey; pk != nil {
		constraintIx[pk.Name] = true
		kind, cols := "", m.indexColumns(plainColumns(pk.Columns))
		if ix := ixs.find(pk.Name); ix != nil {
			kind = " " + ix.Type
			cols = m.indexColumns(ix.Columns)
		}
		defs = append(defs, "CONSTRAINT "+m.QuoteIdent(renamed(pk.Name, src.Object, dst.Object))+" PRIMARY KEY"+kind+" ("+cols+")")
	}
	for _, k := range t.UniqueKeys {
		if t.Options[optIndex(k.Name, "constraint")] != "UNIQUE" {
			continue // a unique index: created below
		}
		constraintIx[k.Name] = true
		kind, cols := "", m.indexColumns(plainColumns(k.Columns))
		if ix := ixs.find(k.Name); ix != nil {
			kind = " " + ix.Type
			cols = m.indexColumns(ix.Columns)
		}
		defs = append(defs, "CONSTRAINT "+m.QuoteIdent(renamed(k.Name, src.Object, dst.Object))+" UNIQUE"+kind+" ("+cols+")")
	}
	var disabledChecks []meta.Check
	for _, ck := range t.Checks {
		if ck.NotEnforced {
			disabledChecks = append(disabledChecks, ck)
			continue
		}
		defs = append(defs, "CONSTRAINT "+m.QuoteIdent(renamed(ck.Name, src.Object, dst.Object))+" CHECK "+parenthesize(ck.Expression))
	}
	out := []string{"CREATE TABLE " + target + " (\n    " + strings.Join(defs, ",\n    ") + "\n)"}

	list := strings.Join(insertCols, ", ")
	insert := "INSERT INTO " + target + " (" + list + ")\nSELECT " + list + " FROM " + m.Qualify(src)
	if identity {
		insert = "SET IDENTITY_INSERT " + target + " ON;\n" + insert + ";\nSET IDENTITY_INSERT " + target + " OFF;"
	}
	out = append(out, insert)

	// Clustered indexes first: creating one rebuilds the nonclustered ones.
	var clustered, other []string
	for _, ix := range t.Indexes {
		if constraintIx[ix.Name] {
			continue
		}
		stmt := m.createIndex(ix, target, t.Options)
		switch {
		case stmt == "":
		case strings.HasPrefix(ix.Type, "CLUSTERED"):
			clustered = append(clustered, stmt)
		default:
			other = append(other, stmt)
		}
	}
	out = append(out, clustered...)
	out = append(out, other...)
	for _, ck := range disabledChecks {
		name := m.QuoteIdent(renamed(ck.Name, src.Object, dst.Object))
		out = append(out,
			"ALTER TABLE "+target+" WITH NOCHECK ADD CONSTRAINT "+name+" CHECK "+parenthesize(ck.Expression),
			"ALTER TABLE "+target+" NOCHECK CONSTRAINT "+name)
	}
	return out, nil
}

func plainColumns(names []string) []meta.IndexColumn {
	out := make([]meta.IndexColumn, len(names))
	for i, n := range names {
		out[i] = meta.IndexColumn{Name: n}
	}
	return out
}

func parenthesize(expr string) string {
	if e := strings.TrimSpace(expr); strings.HasPrefix(e, "(") && stripParens(e) != e {
		return e
	}
	return "(" + expr + ")"
}

// createIndex returns CREATE INDEX for ix on target, or "" for index types
// that are not copied.
func (m *MSSQL) createIndex(ix meta.Index, target string, opts map[string]string) string {
	name := m.QuoteIdent(ix.Name)
	switch ix.Type {
	case "CLUSTERED COLUMNSTORE":
		return "CREATE CLUSTERED COLUMNSTORE INDEX " + name + " ON " + target
	case "NONCLUSTERED COLUMNSTORE":
		var cols []string
		for _, c := range ix.Columns {
			cols = append(cols, m.QuoteIdent(c.Name))
		}
		return "CREATE NONCLUSTERED COLUMNSTORE INDEX " + name + " ON " + target + " (" + strings.Join(cols, ", ") + ")"
	case "CLUSTERED", "NONCLUSTERED":
	default:
		return "" // XML, SPATIAL, HASH, …
	}
	var b strings.Builder
	b.WriteString("CREATE ")
	if ix.Unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString(ix.Type + " INDEX " + name + " ON " + target + " (" + m.indexColumns(ix.Columns) + ")")
	if len(ix.Include) > 0 {
		inc := make([]string, len(ix.Include))
		for i, c := range ix.Include {
			inc[i] = m.QuoteIdent(c)
		}
		b.WriteString(" INCLUDE (" + strings.Join(inc, ", ") + ")")
	}
	if ix.Filter != "" {
		b.WriteString(" WHERE " + ix.Filter)
	}
	if ff := opts[optIndex(ix.Name, "fillfactor")]; ff != "" {
		if _, err := strconv.Atoi(ff); err == nil {
			b.WriteString(" WITH (FILLFACTOR = " + ff + ")")
		}
	}
	return b.String()
}

// TextSearchPredicate returns the quick-search condition (spec 10.5): a
// case-insensitive literal "contains" (CHARINDEX, no LIKE wildcards)
// across the columns of TextSearchColumns, combined with OR. Non-text
// columns are compared through CAST(col AS NVARCHAR(MAX)).
//
// Placeholder contract: param is the go-mssqldb placeholder of the search
// text, "@pN" where N is its 1-based position among the statement's
// arguments ("@p1" when empty). It appears once per searchable column, but
// a named placeholder binds the same argument every time, so the caller
// binds the search text exactly ONCE (unlike MySQL's "?"). With no
// searchable column the predicate is "1 = 0" and references no parameter;
// the caller must then not bind it either, or bind it harmlessly (unused
// @pN arguments are accepted by go-mssqldb).
func (m *MSSQL) TextSearchPredicate(columns []meta.Column, param string) string {
	if param == "" {
		param = "@p1"
	}
	var parts []string
	for _, c := range m.TextSearchColumns(columns) {
		expr := m.QuoteIdent(c.Name)
		switch baseType(c.DataType) {
		case "char", "varchar", "nchar", "nvarchar", "sysname":
		default:
			expr = "CAST(" + expr + " AS NVARCHAR(MAX))"
		}
		parts = append(parts, "CHARINDEX(LOWER("+param+"), LOWER("+expr+")) > 0")
	}
	if len(parts) == 0 {
		return "1 = 0"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// TextSearchParamCount is 1 (a named placeholder binds the same argument at
// every occurrence), or 0 when no column is searchable.
func (m *MSSQL) TextSearchParamCount(columns []meta.Column) int {
	if len(m.TextSearchColumns(columns)) == 0 {
		return 0
	}
	return 1
}

// TextSearchColumns returns the columns TextSearchPredicate searches:
// all except binary, image, rowversion, spatial and vector columns.
func (m *MSSQL) TextSearchColumns(columns []meta.Column) []meta.Column {
	var out []meta.Column
	for _, c := range columns {
		switch baseType(c.DataType) {
		case "binary", "varbinary", "image", "timestamp", "rowversion", "geometry", "geography", "vector":
			continue
		}
		out = append(out, c)
	}
	return out
}

// ReplaceScript returns a runnable script that replaces a view, procedure,
// function or trigger with its current source (spec 9.1), in GO batches:
//
//  1. A guard batch that stops the run (THROW) unless the session is in
//     the object's database — CREATE OR ALTER cannot name a database.
//  2. SET ANSI_NULLS / QUOTED_IDENTIFIER OFF when the module was created
//     with them off (they are stored with the module), restored to ON in
//     a last batch.
//  3. The module text from sys.sql_modules, comments before CREATE kept,
//     with the leading CREATE turned into CREATE OR ALTER (SQL Server 2016
//     SP1+) and the object name rewritten to [schema].[name] (this also
//     repairs names left stale by sp_rename). A trigger's ON table is
//     qualified with the schema when it is not. Servers before 2016 get
//     an IF OBJECT_ID(…) IS NULL stub creation plus ALTER instead.
func (m *MSSQL) ReplaceScript(ctx context.Context, c dialect.Conn, obj meta.Path, kind meta.ObjectKind) (string, error) {
	switch kind {
	case meta.KindView, meta.KindProcedure, meta.KindFunction, meta.KindTrigger:
	default:
		return "", fmt.Errorf("mssql cannot replace objects of kind %q", kind)
	}
	obj, err := m.resolve(ctx, c, obj)
	if err != nil {
		return "", err
	}
	mod, err := m.loadModule(ctx, c, obj, kind)
	if err != nil {
		return "", err
	}
	body, _, err := m.rewriteCreate(mod.definition.String, obj, m.atLeast(13))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "IF DB_NAME() <> %s THROW 50000, %s, 1;\nGO\n", NString(obj.Database),
		NString(fmt.Sprintf("This script replaces %s.%s in database %s; switch to that database first.",
			m.QuoteIdent(obj.Schema), m.QuoteIdent(obj.Object), m.QuoteIdent(obj.Database))))
	restore := false
	if !mod.ansiNulls || !mod.quotedIdent {
		if !mod.ansiNulls {
			b.WriteString("SET ANSI_NULLS OFF;\n")
		}
		if !mod.quotedIdent {
			b.WriteString("SET QUOTED_IDENTIFIER OFF;\n")
		}
		b.WriteString("GO\n")
		restore = true
	}
	if !m.atLeast(13) {
		name := m.QuoteIdent(obj.Schema) + "." + m.QuoteIdent(obj.Object)
		if stub := stubFor(kind, mod.typ, obj, m); stub != "" {
			fmt.Fprintf(&b, "IF OBJECT_ID(%s) IS NULL EXEC (%s);\nGO\n", NString(name), NString(stub))
		}
	}
	b.WriteString(strings.TrimRight(body, " \t\r\n"))
	b.WriteString("\nGO\n")
	if restore {
		b.WriteString("SET ANSI_NULLS ON;\nSET QUOTED_IDENTIFIER ON;\nGO\n")
	}
	return b.String(), nil
}

// stubFor returns a minimal CREATE for the pre-2016 stub-and-ALTER pattern.
func stubFor(kind meta.ObjectKind, typ string, obj meta.Path, m *MSSQL) string {
	name := m.QuoteIdent(obj.Schema) + "." + m.QuoteIdent(obj.Object)
	switch kind {
	case meta.KindView:
		return "CREATE VIEW " + name + " AS SELECT 1 AS stub"
	case meta.KindProcedure:
		return "CREATE PROCEDURE " + name + " AS RETURN 0"
	case meta.KindFunction:
		switch strings.TrimSpace(typ) {
		case "IF":
			return "CREATE FUNCTION " + name + "() RETURNS TABLE AS RETURN SELECT 1 AS stub"
		case "TF":
			return "CREATE FUNCTION " + name + "() RETURNS @t TABLE (stub int) AS BEGIN RETURN END"
		}
		return "CREATE FUNCTION " + name + "() RETURNS int AS BEGIN RETURN 0 END"
	}
	return "" // triggers: ALTER needs the table; the stub is skipped
}

// rewriteCreate turns the leading CREATE of a module into CREATE OR ALTER
// (or ALTER without orAlter) and qualifies the object name (and a
// trigger's table) with the schema. Text before CREATE is kept.
func (m *MSSQL) rewriteCreate(src string, obj meta.Path, orAlter bool) (string, string, error) {
	toks := code(src)
	if len(toks) == 0 || !isWord(toks[0], "CREATE") {
		return "", "", fmt.Errorf("unexpected source for %s: does not start with CREATE", m.Qualify(obj))
	}
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	i := 1
	if i+1 < len(toks) && isWord(toks[i], "OR") && isWord(toks[i+1], "ALTER") {
		i += 2
		if !orAlter {
			edits = append(edits, edit{toks[0].Start, toks[2].End, "ALTER"})
		}
	} else if orAlter {
		edits = append(edits, edit{toks[0].End, toks[0].End, " OR ALTER"})
	} else {
		edits = append(edits, edit{toks[0].Start, toks[0].End, "ALTER"})
	}
	if i >= len(toks) {
		return "", "", fmt.Errorf("unexpected source for %s: no object keyword", m.Qualify(obj))
	}
	keyword := strings.ToUpper(toks[i].Text)
	if keyword == "PROC" {
		keyword = "PROCEDURE"
	}
	switch keyword {
	case "VIEW", "PROCEDURE", "FUNCTION", "TRIGGER":
	default:
		return "", "", fmt.Errorf("unexpected source for %s: CREATE %s", m.Qualify(obj), toks[i].Text)
	}
	// nameSpan returns the end index (exclusive) of a dotted name at j.
	nameSpan := func(j int) int {
		k := j
		for k < len(toks) {
			if toks[k].Kind != sqltext.TokIdent && toks[k].Kind != sqltext.TokQuotedIdent {
				break
			}
			k++
			if k < len(toks) && toks[k].Text == "." {
				k++
				continue
			}
			break
		}
		return k
	}
	j := i + 1
	k := nameSpan(j)
	if k == j {
		return "", "", fmt.Errorf("unexpected source for %s: no %s name", m.Qualify(obj), keyword)
	}
	edits = append(edits, edit{toks[j].Start, toks[k-1].End, m.QuoteIdent(obj.Schema) + "." + m.QuoteIdent(obj.Object)})
	if keyword == "TRIGGER" {
		for on := k; on < len(toks); on++ {
			if isWord(toks[on], "ON") {
				t0 := on + 1
				t1 := nameSpan(t0)
				if t1 == t0+1 && !isWord(toks[t0], "DATABASE") && !isWord(toks[t0], "ALL") {
					edits = append(edits, edit{toks[t0].Start, toks[t0].Start, m.QuoteIdent(obj.Schema) + "."})
				}
				break
			}
		}
	}
	out := src
	for e := len(edits) - 1; e >= 0; e-- {
		ed := edits[e]
		out = out[:ed.start] + ed.text + out[ed.end:]
	}
	return out, keyword, nil
}
