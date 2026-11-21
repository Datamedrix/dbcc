package postgres

import (
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

// SQLSTATE codes mapped by MapError (spec 11).
const (
	codeNotNull          = "23502"
	codeForeignKey       = "23503"
	codeUnique           = "23505"
	codeCheck            = "23514"
	codeExclusion        = "23P01"
	codeStringTooLong    = "22001"
	codeNumericRange     = "22003"
	codeInvalidDatetime  = "22007"
	codeDatetimeOverflow = "22008"
	codeInvalidText      = "22P02"
	codeGeneratedAlways  = "428C9" // cannot insert a non-DEFAULT value into column "c"
)

var (
	reDetailKey  = regexp.MustCompile(`^Key \((.*?)\)=`)
	reColumnName = regexp.MustCompile(`column "((?:[^"]|"")*)"`)
	reForType    = regexp.MustCompile(`for type ([^:]+?)(?::|$)`)
	reTooLong    = regexp.MustCompile(`value too long for type (.+)$`)
	reRangeType  = regexp.MustCompile(`out of range for type (\w+)`)
)

// MapError maps a server error to the column or constraint it concerns
// (spec 11). It returns nil for errors that are not PostgreSQL server
// errors or not related to a row's values.
//
//   - 23502 not null: PgError.ColumnName.
//   - 23505 unique, 23503 foreign key, 23P01 exclusion: ConstraintName; the
//     column is the first one of "Key (a, b)=…" in the detail (the columns
//     of the table being written, also when a parent row is deleted), else
//     the first column of the constraint in t.
//   - 23514 check: ConstraintName (or ColumnName for domain checks); the
//     column is the single column of t the check expression references.
//   - 428C9 (value for an identity ALWAYS or generated column): the column
//     from the message.
//   - 22001, 22003, 22P02, 22007, 22008 (bad value): PostgreSQL does not
//     name the column. ColumnName is used when present; otherwise, with t,
//     the column is derived from the type in the message when exactly one
//     column of t has that type ("value too long for type character
//     varying(20)", "invalid input syntax for type integer").
//
// FieldError.Message is the server message plus its detail in brackets.
func (p *Postgres) MapError(err error, t *meta.Table) *dialect.FieldError {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return nil
	}
	msg := pe.Message
	if pe.Detail != "" {
		msg += " (" + pe.Detail + ")"
	}
	fe := &dialect.FieldError{Message: msg, Err: err, Constraint: pe.ConstraintName}
	switch pe.Code {
	case codeNotNull:
		fe.Column = pe.ColumnName
	case codeUnique, codeForeignKey, codeExclusion:
		if sm := reDetailKey.FindStringSubmatch(pe.Detail); sm != nil {
			fe.Column = firstIdent(sm[1])
		}
		if fe.Column == "" && t != nil {
			fe.Column = first(t.ConstraintColumns(pe.ConstraintName))
		}
	case codeCheck:
		fe.Column = pe.ColumnName
		if fe.Column == "" {
			fe.Column = checkColumn(t, pe.ConstraintName)
		}
	case codeGeneratedAlways:
		fe.Column = pe.ColumnName
		if sm := reColumnName.FindStringSubmatch(pe.Message); fe.Column == "" && sm != nil {
			fe.Column = strings.ReplaceAll(sm[1], `""`, `"`)
		}
	case codeStringTooLong, codeNumericRange, codeInvalidText, codeInvalidDatetime, codeDatetimeOverflow:
		fe.Column = pe.ColumnName
		if fe.Column == "" {
			fe.Column = columnByType(t, pe.Code, pe.Message)
		}
	default:
		return nil
	}
	return fe
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// firstIdent returns the first identifier of a "a, b" or "\"A\", b" list
// as it appears in error details (quoted only when needed).
func firstIdent(list string) string {
	for _, tok := range sqltext.NewPostgres().Tokenize(list) {
		switch tok.Kind {
		case sqltext.TokIdent:
			return tok.Text
		case sqltext.TokQuotedIdent:
			return strings.ReplaceAll(tok.Text[1:len(tok.Text)-1], `""`, `"`)
		case sqltext.TokWhitespace:
			continue
		}
		// Expression index keys such as lower(name): no single column.
		return ""
	}
	return ""
}

// checkColumn returns the column a check constraint of t refers to when
// its expression references exactly one column.
func checkColumn(t *meta.Table, name string) string {
	if t == nil {
		return ""
	}
	var expr string
	for _, ck := range t.Checks {
		if ck.Name == name {
			expr = ck.Expression
		}
	}
	if expr == "" {
		return ""
	}
	found := ""
	for _, tok := range sqltext.NewPostgres().Tokenize(expr) {
		var n string
		switch tok.Kind {
		case sqltext.TokIdent:
			n = tok.Text
		case sqltext.TokQuotedIdent:
			n = strings.ReplaceAll(tok.Text[1:len(tok.Text)-1], `""`, `"`)
		default:
			continue
		}
		var c *meta.Column
		for i := range t.Columns {
			if t.Columns[i].Name == n || (tok.Kind == sqltext.TokIdent && strings.EqualFold(t.Columns[i].Name, n)) {
				c = &t.Columns[i]
				break
			}
		}
		if c == nil {
			continue
		}
		if found != "" && found != c.Name {
			return ""
		}
		found = c.Name
	}
	return found
}

// columnByType derives the column of a value error from the type named in
// the message, when exactly one insertable column of t matches.
func columnByType(t *meta.Table, code, msg string) string {
	if t == nil {
		return ""
	}
	var match func(dataType string) bool
	switch code {
	case codeStringTooLong:
		sm := reTooLong.FindStringSubmatch(msg)
		if sm == nil {
			return ""
		}
		want := strings.TrimSpace(sm[1])
		match = func(dt string) bool { return dt == want }
	case codeNumericRange:
		l := strings.ToLower(msg)
		if sm := reRangeType.FindStringSubmatch(l); sm != nil {
			want := sm[1]
			match = func(dt string) bool { return dt == want }
			break
		}
		switch {
		case strings.HasPrefix(l, "smallint out of range"):
			match = func(dt string) bool { return dt == "smallint" }
		case strings.HasPrefix(l, "integer out of range"):
			match = func(dt string) bool { return dt == "integer" }
		case strings.HasPrefix(l, "bigint out of range"):
			match = func(dt string) bool { return dt == "bigint" }
		case strings.Contains(l, "numeric field overflow"):
			match = func(dt string) bool { return strings.HasPrefix(dt, "numeric") }
		default:
			return ""
		}
	case codeInvalidDatetime, codeDatetimeOverflow:
		if sm := reForType.FindStringSubmatch(msg); sm != nil {
			want := strings.TrimSpace(sm[1])
			match = func(dt string) bool { return baseType(dt) == want }
		} else {
			match = func(dt string) bool {
				b := baseType(dt)
				return b == "date" || strings.HasPrefix(b, "timestamp") || strings.HasPrefix(b, "time")
			}
		}
	default: // 22P02
		sm := reForType.FindStringSubmatch(msg)
		if sm == nil {
			return ""
		}
		want := strings.TrimSpace(sm[1])
		match = func(dt string) bool { return baseType(dt) == want || dt == want }
	}
	found := ""
	for _, c := range t.Columns {
		if c.Generated != nil || !match(c.DataType) {
			continue
		}
		if found != "" {
			return "" // ambiguous
		}
		found = c.Name
	}
	return found
}
