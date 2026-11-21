package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
)

// CopyTable returns the statements of cp (spec 8.2).
//
//   - Data only: CREATE TABLE dst AS SELECT * FROM src (a view becomes a
//     snapshot table, A2).
//   - With structure, table: CREATE TABLE dst (LIKE src INCLUDING ALL),
//     which copies columns, defaults, NOT NULL, CHECK constraints, primary
//     key, unique constraints, indexes, identity and generated columns,
//     comments, storage and statistics, but neither foreign keys (A1) nor
//     triggers nor partitioning (a partitioned source becomes a plain
//     table). Then:
//   - serial columns get their own sequence (LIKE would make dst share
//     the source's sequence): CREATE SEQUENCE dst_col_seq AS <type> OWNED
//     BY dst.col and ALTER TABLE dst ALTER col SET DEFAULT nextval(…);
//   - INSERT INTO dst (cols) [OVERRIDING SYSTEM VALUE] SELECT cols FROM
//     src, without generated columns; OVERRIDING SYSTEM VALUE when there
//     are identity columns, so their values are kept;
//   - SELECT setval(…) per identity and serial column, so new rows do not
//     collide with the copied ones.
//   - With structure, view or materialized view: a second view
//     (materialized view) with the same definition (A2); t must come from
//     DescribeTable (ViewDefinition).
//
// t.Path.Schema / dst.Schema empty means the session's search_path.
func (p *Postgres) CopyTable(src, dst meta.Path, t *meta.Table, withStructure bool) ([]string, error) {
	if src.Object == "" || dst.Object == "" {
		return nil, fmt.Errorf("copy needs a source and a target name")
	}
	s, d := p.Qualify(src), p.Qualify(dst)
	if !withStructure {
		return []string{"CREATE TABLE " + d + " AS SELECT * FROM " + s}, nil
	}
	if t == nil {
		return nil, fmt.Errorf("copy with structure needs the metadata of %s", s)
	}
	switch t.Kind {
	case meta.KindView, meta.KindMaterializedView:
		if t.ViewDefinition == "" {
			return nil, fmt.Errorf("no view definition for %s", s)
		}
		with := ""
		if o := t.Options["options"]; o != "" {
			with = " WITH (" + o + ")"
		}
		if t.Kind == meta.KindView {
			return []string{"CREATE VIEW " + d + with + " AS\n" + t.ViewDefinition}, nil
		}
		return []string{"CREATE MATERIALIZED VIEW " + d + with + " AS\n" + t.ViewDefinition}, nil
	}
	stmts := []string{"CREATE TABLE " + d + " (LIKE " + s + " INCLUDING ALL)"}
	var cols, seqCols []string
	identity := false
	for _, c := range t.Columns {
		if c.Generated != nil {
			continue
		}
		cols = append(cols, p.QuoteIdent(c.Name))
		if t.Options["identity."+c.Name] != "" {
			identity = true
			seqCols = append(seqCols, c.Name)
			continue
		}
		if c.AutoIncrement && c.Default != nil && c.DefaultExpr && strings.HasPrefix(*c.Default, "nextval(") {
			seq := dst
			seq.Object = truncIdent(dst.Object + "_" + c.Name + "_seq")
			as := ""
			switch strings.ToLower(c.DataType) {
			case "smallint", "integer", "bigint":
				as = " AS " + strings.ToLower(c.DataType)
			}
			stmts = append(stmts,
				"CREATE SEQUENCE "+p.Qualify(seq)+as+" OWNED BY "+d+"."+p.QuoteIdent(c.Name),
				"ALTER TABLE "+d+" ALTER COLUMN "+p.QuoteIdent(c.Name)+" SET DEFAULT nextval("+quoteLiteral(p.Qualify(seq))+"::regclass)")
			seqCols = append(seqCols, c.Name)
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("%s has no insertable columns", s)
	}
	list := strings.Join(cols, ", ")
	ins := "INSERT INTO " + d + " (" + list + ")"
	if identity {
		ins += " OVERRIDING SYSTEM VALUE"
	}
	stmts = append(stmts, ins+" SELECT "+list+" FROM "+s)
	for _, c := range seqCols {
		stmts = append(stmts, fmt.Sprintf("SELECT setval(pg_get_serial_sequence(%s, %s), max(%s)) FROM %s",
			quoteLiteral(d), quoteLiteral(c), p.QuoteIdent(c), d))
	}
	return stmts, nil
}

// truncIdent cuts a generated name to PostgreSQL's 63-byte identifier limit
// without splitting a UTF-8 sequence.
func truncIdent(s string) string {
	if len(s) <= 63 {
		return s
	}
	s = s[:63]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// quoteLiteral returns s as a standard SQL string literal.
func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ReplaceScript returns a runnable script that re-creates the object from
// its current source (spec 9.1). Every statement ends with ";" and the
// script splits correctly with sqltext.NewPostgres (bodies are
// dollar-quoted by pg_get_functiondef). Object names are schema-qualified,
// so the script does not depend on the session's search_path.
//
//   - view: CREATE OR REPLACE VIEW (PostgreSQL only allows adding columns
//     at the end; other changes need DROP VIEW first).
//   - function, procedure: CREATE OR REPLACE for every overload.
//   - trigger: CREATE OR REPLACE TRIGGER on PostgreSQL 14+; constraint
//     triggers and older servers get DROP TRIGGER IF EXISTS … ON table
//     followed by CREATE TRIGGER.
//   - materialized view: DROP MATERIALIZED VIEW IF EXISTS, CREATE …, its
//     indexes and its comment (no CREATE OR REPLACE exists).
func (p *Postgres) ReplaceScript(ctx context.Context, c dialect.Conn, obj meta.Path, kind meta.ObjectKind) (string, error) {
	if _, err := p.checkDatabase(ctx, c, obj.Database); err != nil {
		return "", err
	}
	schema, err := p.schemaOf(ctx, c, obj)
	if err != nil {
		return "", err
	}
	obj.Schema = schema
	switch kind {
	case meta.KindView:
		src, err := p.ObjectSource(ctx, c, obj, kind)
		if err != nil {
			return "", err
		}
		return "CREATE OR REPLACE" + strings.TrimPrefix(src, "CREATE") + ";\n", nil
	case meta.KindFunction, meta.KindProcedure:
		defs, err := p.routineDefs(ctx, c, schema, obj.Object, kind)
		if err != nil {
			return "", err
		}
		return strings.Join(defs, ";\n\n") + ";\n", nil
	case meta.KindTrigger:
		trs, err := p.triggerDefs(ctx, c, schema, obj.Object)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, tr := range trs {
			if p.atLeast(14) && !tr.constraint && strings.HasPrefix(tr.def, "CREATE TRIGGER ") {
				b.WriteString("CREATE OR REPLACE TRIGGER " + strings.TrimPrefix(tr.def, "CREATE TRIGGER ") + ";\n")
				continue
			}
			fmt.Fprintf(&b, "DROP TRIGGER IF EXISTS %s ON %s;\n%s;\n", p.QuoteIdent(obj.Object), tr.table, tr.def)
		}
		return b.String(), nil
	case meta.KindMaterializedView:
		src, err := p.ObjectSource(ctx, c, obj, kind)
		if err != nil {
			return "", err
		}
		r, err := p.findRel(ctx, c, schema, obj.Object, "m")
		if err != nil {
			return "", err
		}
		q := p.Qualify(obj)
		var b strings.Builder
		fmt.Fprintf(&b, "DROP MATERIALIZED VIEW IF EXISTS %s;\n%s;\n", q, src)
		rows, err := c.QueryContext(ctx, p.qualifiedCTE(c)+`SELECT CASE WHEN dbcc_sp.v IS NOT NULL THEN pg_get_indexdef(ix.indexrelid) END
			FROM dbcc_sp, pg_index ix JOIN pg_class ic ON ic.oid = ix.indexrelid
			WHERE ix.indrelid = $1::int8::oid ORDER BY ic.relname`, r.oid)
		if err != nil {
			return "", err
		}
		idx, err := scanStrings(rows)
		if err != nil {
			return "", err
		}
		for _, ix := range idx {
			b.WriteString(ix + ";\n")
		}
		if r.comment != "" {
			fmt.Fprintf(&b, "COMMENT ON MATERIALIZED VIEW %s IS %s;\n", q, quoteLiteral(r.comment))
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("postgres cannot replace objects of kind %q", kind)
}

// TextSearchPredicate returns the quick-search condition (spec 10.5): a
// case-insensitive literal "contains" (strpos, no LIKE wildcards) across
// all searchable columns, combined with OR. Non-text columns are compared
// through their text representation (col::text); bytea columns are
// skipped.
//
// Parameter contract (differs from MySQL): param is the placeholder ("$1"
// when empty) and is used for every column, so the caller binds the search
// text ONCE (TextSearchParamCount). When combining with other bound
// conditions, pass Placeholder(n) for the next free number. With no
// searchable column the predicate is "1 = 0" and takes no parameter.
func (p *Postgres) TextSearchPredicate(columns []meta.Column, param string) string {
	if param == "" {
		param = "$1"
	}
	var parts []string
	for _, c := range p.TextSearchColumns(columns) {
		expr := p.QuoteIdent(c.Name)
		if !isTextType(c.DataType) {
			expr += "::text"
		}
		parts = append(parts, "strpos(lower("+expr+"), lower("+param+"::text)) > 0")
	}
	if len(parts) == 0 {
		return "1 = 0"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// TextSearchColumns returns the columns TextSearchPredicate searches.
func (p *Postgres) TextSearchColumns(columns []meta.Column) []meta.Column {
	var out []meta.Column
	for _, c := range columns {
		if baseType(c.DataType) != "bytea" {
			out = append(out, c)
		}
	}
	return out
}

// TextSearchParamCount is the number of arguments to bind for
// TextSearchPredicate: 1, or 0 when no column is searchable.
func (p *Postgres) TextSearchParamCount(columns []meta.Column) int {
	if len(p.TextSearchColumns(columns)) == 0 {
		return 0
	}
	return 1
}

// Placeholder returns the bound-parameter placeholder number n (1-based).
func (p *Postgres) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

// Rebind replaces "?" placeholders by $1, $2, … in order, skipping
// strings, quoted identifiers and comments (dialect.Rebind). Callers use it
// only for statements that are executed with arguments (a lone "?" is also
// the jsonb key-exists operator). Existing $n placeholders are left alone.
func (p *Postgres) Rebind(query string) string { return dialect.Rebind(p, query) }

// InsertDefaults is the standard all-defaults INSERT.
func (p *Postgres) InsertDefaults(qualifiedTable string) string {
	return "INSERT INTO " + qualifiedTable + " DEFAULT VALUES"
}

func baseType(dataType string) string {
	t := strings.ToLower(strings.TrimSpace(dataType))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t
}

func isTextType(dataType string) bool {
	switch baseType(dataType) {
	case "text", "character varying", "character", "varchar", "char", "bpchar", "name", "citext":
		return !strings.HasSuffix(strings.TrimSpace(dataType), "]")
	}
	return false
}
