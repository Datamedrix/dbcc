package grid

import (
	"strings"

	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
)

// Requery runs a generated query for a table-origin result (spec 10.4,
// 10.5) and returns the new lazily loaded result. It is called through the
// grid's Fetcher, i.e. on the application's DB worker; the application
// typically implements it as
//
//	func(q string, args []any) (*exec.Result, error) {
//		return runner.Grid(ctx, q, res.Origin, args...)
//	}
type Requery func(sql string, args []any) (*exec.Result, error)

// QueryBuilder generates the server-side query of a table-origin result.
type QueryBuilder struct {
	Dialect dialect.Dialect
	// From is the qualified object (Origin.SQL, or Dialect.Qualify(Origin.Object)).
	From string
	// Columns are the result columns (SELECT * on the object).
	Columns []exec.ColumnInfo
	// Table is the object's metadata, optional; it gives exact data types
	// for the quick search.
	Table *meta.Table
	// Placeholder returns the n-th (1-based) bind placeholder; nil uses
	// Dialect.Placeholder.
	Placeholder func(n int) string
}

// NewQueryBuilder returns a builder for res, or nil when res is not table-origin.
func NewQueryBuilder(d dialect.Dialect, res *exec.Result) *QueryBuilder {
	if d == nil || res == nil || res.Origin == nil {
		return nil
	}
	from := res.Origin.SQL
	if from == "" {
		if res.Origin.Object.Object == "" {
			return nil
		}
		from = d.Qualify(res.Origin.Object)
	}
	return &QueryBuilder{Dialect: d, From: from, Columns: res.Columns, Table: res.Origin.Table}
}

func (b *QueryBuilder) placeholder(n int) string {
	if b.Placeholder != nil {
		return b.Placeholder(n)
	}
	return b.Dialect.Placeholder(n)
}

func (b *QueryBuilder) metaColumn(col int) meta.Column {
	name := b.Columns[col].Name
	if b.Table != nil {
		if c := b.Table.Column(name); c != nil {
			return *c
		}
	}
	return meta.Column{Name: name, Position: col + 1, DataType: strings.ToLower(b.Columns[col].DBType)}
}

// searchPredicate is the dialect's quick-search predicate over cols with
// its bound arguments: the text Dialect.TextSearchParamCount times.
func (b *QueryBuilder) searchPredicate(cols []meta.Column, text string, next int) (string, []any) {
	pred := b.Dialect.TextSearchPredicate(cols, b.placeholder(next))
	args := make([]any, b.Dialect.TextSearchParamCount(cols))
	for i := range args {
		args[i] = text
	}
	return pred, args
}

// likeEscape escapes LIKE wildcards with '!' (used with ESCAPE '!').
func likeEscape(s string) string {
	r := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return r.Replace(s)
}

// Build returns the query for st and its arguments:
//
//	SELECT * FROM <from> [WHERE <search> AND <filter>… AND (<where>)] [ORDER BY <col> ASC|DESC, …]
//
// The quick search uses Dialect.TextSearchPredicate; column filters use
// bound parameters ("contains" goes through TextSearchPredicate on the one
// column, "starts with" is LIKE … ESCAPE '!'); the free WHERE is inserted
// verbatim in parentheses. Identifiers are quoted with Dialect.QuoteIdent.
func (b *QueryBuilder) Build(st State) (string, []any) {
	var conds []string
	var args []any
	q := b.Dialect.QuoteIdent
	if st.Search != "" {
		cols := make([]meta.Column, len(b.Columns))
		if b.Table != nil && len(b.Table.Columns) > 0 {
			cols = b.Table.Columns
		} else {
			for i := range b.Columns {
				cols[i] = b.metaColumn(i)
			}
		}
		pred, a := b.searchPredicate(cols, st.Search, len(args)+1)
		conds = append(conds, pred)
		args = append(args, a...)
	}
	for _, f := range st.Filters {
		if f.Col < 0 || f.Col >= len(b.Columns) {
			continue
		}
		col := q(b.Columns[f.Col].Name)
		ph := b.placeholder(len(args) + 1)
		var c string
		switch f.Op {
		case OpContains:
			pred, a := b.searchPredicate([]meta.Column{b.metaColumn(f.Col)}, f.Value, len(args)+1)
			conds = append(conds, pred)
			args = append(args, a...)
			continue
		case OpStartsWith:
			c = col + " LIKE " + ph + " ESCAPE '!'"
			args = append(args, likeEscape(f.Value)+"%")
		case OpEquals, OpNotEquals, OpLess, OpLessEq, OpGreater, OpGreaterEq:
			c = col + " " + f.Op.String() + " " + ph
			args = append(args, f.Value)
		case OpIsNull:
			c = col + " IS NULL"
		case OpNotNull:
			c = col + " IS NOT NULL"
		default:
			continue
		}
		conds = append(conds, c)
	}
	if w := strings.TrimSpace(st.Where); w != "" {
		conds = append(conds, "("+w+")")
	}
	var s strings.Builder
	s.WriteString("SELECT * FROM ")
	s.WriteString(b.From)
	if len(conds) > 0 {
		s.WriteString(" WHERE ")
		s.WriteString(strings.Join(conds, " AND "))
	}
	if len(st.Sort) > 0 {
		s.WriteString(" ORDER BY ")
		for i, k := range st.Sort {
			if i > 0 {
				s.WriteString(", ")
			}
			s.WriteString(q(b.Columns[k.Col].Name))
			if k.Desc {
				s.WriteString(" DESC")
			} else {
				s.WriteString(" ASC")
			}
		}
	}
	return s.String(), args
}
