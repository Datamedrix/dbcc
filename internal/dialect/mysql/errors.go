package mysql

import (
	"errors"
	"regexp"
	"strings"

	driver "github.com/go-sql-driver/mysql"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
)

// Server error numbers mapped by MapError (spec 11). Verified against MySQL
// 8.4 and MariaDB 11 by the integration tests.
const (
	erBadNullError         = 1048 // Column 'c' cannot be null
	erDupEntry             = 1062 // Duplicate entry 'v' for key 'k' (MySQL 8: 't.k')
	erWarnNullToNotNull    = 1263 // Column set to default value; NULL supplied to NOT NULL column 'c'
	erWarnDataOutOfRange   = 1264 // Out of range value for column 'c'
	erWarnDataTruncated    = 1265 // Data truncated for column 'c'
	erTruncatedWrongValue  = 1292 // Incorrect datetime value: 'v' for column 'c'
	erNoDefaultForField    = 1364 // Field 'c' doesn't have a default value
	erTruncWrongValueField = 1366 // Incorrect integer value: 'v' for column 'c'
	erDataTooLong          = 1406 // Data too long for column 'c'
	erRowIsReferenced2     = 1451 // Cannot delete or update a parent row: a foreign key constraint fails (…)
	erNoReferencedRow2     = 1452 // Cannot add or update a child row: a foreign key constraint fails (…)
	erNonDefaultGenerated  = 3105 // The value specified for generated column 'c' in table 't' is not allowed
	erGeneratedIgnored     = 1906 // MariaDB: The value specified for generated column 'c' in table 't' has been ignored
	erCheckViolatedMySQL   = 3819 // Check constraint 'n' is violated.
	erCheckViolatedMariaDB = 4025 // CONSTRAINT `n` failed for `db`.`t`
	erRowIsReferenced      = 1217 // older variants without constraint details
	erNoReferencedRow      = 1216
)

// errNumber returns the MySQL server error number of err, or 0.
func errNumber(err error) uint16 {
	var me *driver.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

var (
	// 'col' or `db`.`table`.`col` (MariaDB 1366/1292 use the qualified form).
	reForColumn  = regexp.MustCompile("(?i)(?:column|field) (?:'((?:[^']|'')*)'|((?:`(?:[^`]|``)*`\\.?)+))")
	reForKey     = regexp.MustCompile(`for key '((?:[^']|'')*)'`)
	reFKDetail   = regexp.MustCompile("CONSTRAINT `((?:[^`]|``)*)` FOREIGN KEY \\(([^)]*)\\) REFERENCES (`(?:[^`]|``)*`(?:\\.`(?:[^`]|``)*`)?) \\(([^)]*)\\)")
	reCheckMySQL = regexp.MustCompile(`Check constraint '((?:[^']|'')*)' is violated`)
	reCheckMaria = regexp.MustCompile("CONSTRAINT `((?:[^`]|``)*)` failed")
	reBackticked = regexp.MustCompile("`((?:[^`]|``)*)`")
)

// MapError maps a server error to the column or constraint it concerns
// (spec 11). It returns nil for errors that are not MySQL server errors or
// not related to a single row's values. t, if given, resolves constraint
// names to columns; FieldError.Column is then the first column of the
// constraint, and meta.Table.ConstraintColumns returns all of them.
func (m *MySQL) MapError(err error, t *meta.Table) *dialect.FieldError {
	var me *driver.MySQLError
	if !errors.As(err, &me) {
		return nil
	}
	fe := &dialect.FieldError{Message: me.Message, Err: err}
	msg := me.Message
	switch me.Number {
	case erBadNullError, erNoDefaultForField, erDataTooLong, erWarnDataOutOfRange,
		erTruncWrongValueField, erTruncatedWrongValue, erWarnDataTruncated, erWarnNullToNotNull,
		erNonDefaultGenerated, erGeneratedIgnored:
		fe.Column = columnFromMessage(msg)
	case erDupEntry:
		if sm := reForKey.FindStringSubmatch(msg); sm != nil {
			// MySQL 8.0.19+ qualifies the key with the table name.
			key := unqualify(strings.ReplaceAll(sm[1], "''", "'"), t, t.ConstraintColumns)
			fe.Constraint = key
			fe.Column = first(t.ConstraintColumns(key))
		}
	case erRowIsReferenced2, erNoReferencedRow2:
		if sm := reFKDetail.FindStringSubmatch(msg); sm != nil {
			fe.Constraint = unbacktick(sm[1])
			cols := identList(sm[2])
			if me.Number == erRowIsReferenced2 {
				// Deleting or updating the parent: t is the referenced table.
				cols = identList(sm[4])
			}
			fe.Column = first(cols)
		}
	case erRowIsReferenced, erNoReferencedRow:
		// No constraint details in the message.
	case erCheckViolatedMySQL, erCheckViolatedMariaDB:
		re := reCheckMySQL
		if me.Number == erCheckViolatedMariaDB {
			re = reCheckMaria
		}
		if sm := re.FindStringSubmatch(msg); sm != nil {
			name := strings.ReplaceAll(sm[1], "''", "'")
			if me.Number == erCheckViolatedMariaDB {
				name = strings.ReplaceAll(sm[1], "``", "`")
			}
			// MariaDB names column-level checks "table.column" in the message.
			fe.Constraint = unqualify(name, t, func(n string) []string {
				if hasCheck(t, n) {
					return []string{n}
				}
				return nil
			})
			fe.Column = checkColumn(t, fe.Constraint)
		}
	default:
		return nil
	}
	return fe
}

// unqualify strips a "table." prefix from a constraint name in a message,
// unless the full name is known to t (known returns non-nil for it).
func unqualify(name string, t *meta.Table, known func(string) []string) string {
	if t != nil && known(name) != nil {
		return name
	}
	if t != nil {
		if p := t.Path.Object + "."; strings.HasPrefix(name, p) {
			return name[len(p):]
		}
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func hasCheck(t *meta.Table, name string) bool {
	if t == nil {
		return false
	}
	for _, ck := range t.Checks {
		if strings.EqualFold(ck.Name, name) {
			return true
		}
	}
	return false
}

// columnFromMessage extracts the column from "column 'c'", "Field 'c'" or
// "column `db`.`t`.`c`".
func columnFromMessage(msg string) string {
	sm := reForColumn.FindStringSubmatch(msg)
	if sm == nil {
		return ""
	}
	if sm[1] != "" {
		return strings.ReplaceAll(sm[1], "''", "'")
	}
	parts := reBackticked.FindAllStringSubmatch(sm[2], -1)
	if len(parts) == 0 {
		return ""
	}
	return strings.ReplaceAll(parts[len(parts)-1][1], "``", "`")
}

// checkColumn derives the column of a check constraint: a column-level
// MariaDB check is named after its column; otherwise the expression must
// reference exactly one column of t.
func checkColumn(t *meta.Table, name string) string {
	if t == nil {
		return ""
	}
	var expr string
	for _, ck := range t.Checks {
		if strings.EqualFold(ck.Name, name) {
			expr = ck.Expression
		}
	}
	if expr == "" {
		if c := t.Column(name); c != nil {
			return c.Name
		}
		return ""
	}
	found := ""
	for _, sm := range reBackticked.FindAllStringSubmatch(expr, -1) {
		c := t.Column(strings.ReplaceAll(sm[1], "``", "`"))
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

func identList(s string) []string {
	var out []string
	for _, sm := range reBackticked.FindAllStringSubmatch(s, -1) {
		out = append(out, strings.ReplaceAll(sm[1], "``", "`"))
	}
	return out
}

func unbacktick(s string) string { return strings.ReplaceAll(s, "``", "`") }

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
