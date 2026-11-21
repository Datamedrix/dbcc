package mssql

import (
	"errors"
	"regexp"
	"strings"

	driver "github.com/microsoft/go-mssqldb"

	"dbcc/internal/dialect"
	"dbcc/internal/meta"
)

// Server error numbers mapped by MapError (spec 11), verified against SQL
// Server 2022 by TestMapError.
const (
	errNull            = 515  // Cannot insert the value NULL into column 'c', table 'db.s.t'; …
	errFKOrCheck       = 547  // The INSERT statement conflicted with the FOREIGN KEY|CHECK|REFERENCE constraint "n". …
	errDupKey          = 2627 // Violation of PRIMARY KEY|UNIQUE KEY constraint 'n'. Cannot insert duplicate key …
	errDupIndex        = 2601 // Cannot insert duplicate key row in object 's.t' with unique index 'n'. …
	errTruncated       = 8152 // String or binary data would be truncated. (no column)
	errTruncatedColumn = 2628 // String or binary data would be truncated in table 'db.s.t', column 'c'. …
	errOverflow        = 220  // Arithmetic overflow error for data type tinyint, value = 300.
	errOverflowConvert = 8115 // Arithmetic overflow error converting expression to data type int.
	errConvert         = 245  // Conversion failed when converting the varchar value 'x' to data type int.
	errConvertType     = 8114 // Error converting data type nvarchar to numeric.
	errDateString      = 241  // Conversion failed when converting date and/or time from character string.
	errDateRange       = 242  // The conversion of a varchar data type to a datetime data type resulted in an out-of-range value.
	errIdentityInsert  = 544  // Cannot insert explicit value for identity column in table 't' when IDENTITY_INSERT is set to OFF.
	errComputed        = 271  // The column "c" cannot be modified because it is either a computed column or …
	errIdentityUpdate  = 8102 // Cannot update identity column 'c'.
	errRowversion      = 273  // Cannot insert an explicit value into a timestamp column. …
)

var (
	reColumnQ     = regexp.MustCompile(`column '((?:[^']|'')*)'`)
	reColumnDQ    = regexp.MustCompile(`column "((?:[^"]|"")*)"`)
	reConstraintQ = regexp.MustCompile(`constraint '((?:[^']|'')*)'`)
	reConstraint  = regexp.MustCompile(`(FOREIGN KEY|REFERENCE|CHECK) (?:SAME TABLE )?constraint "((?:[^"]|"")*)"`)
	reUniqueIndex = regexp.MustCompile(`unique index '((?:[^']|'')*)'`)
	reBracketed   = regexp.MustCompile(`\[((?:[^\]]|\]\])*)\]`)
)

// serverErrors returns the server errors carried by err, first to last.
func serverErrors(err error) []driver.Error {
	var me driver.Error
	if !errors.As(err, &me) {
		var pe *driver.Error
		if !errors.As(err, &pe) || pe == nil {
			return nil
		}
		me = *pe
	}
	if len(me.All) > 0 {
		return me.All
	}
	return []driver.Error{me}
}

// MapError maps a server error to the column or constraint it concerns
// (spec 11). go-mssqldb reports all messages of a failed statement; the
// last one is usually 3621 "The statement has been terminated.", so the
// first error with a known number is used. t, if given, resolves
// constraint and index names to columns (FieldError.Column is the first
// column; meta.Table.ConstraintColumns returns all of them).
//
// Numbers without a column in the message (8152, 220, 8115, 245, 8114,
// 241, 242) still return a FieldError with the message, so the form can
// show it; Column is then empty. 2628 (SQL Server 2019+, compatibility
// level 150) names the column.
func (m *MSSQL) MapError(err error, t *meta.Table) *dialect.FieldError {
	for _, e := range serverErrors(err) {
		if fe := mapOne(e, t); fe != nil {
			fe.Err = err
			return fe
		}
	}
	return nil
}

func mapOne(e driver.Error, t *meta.Table) *dialect.FieldError {
	msg := e.Message
	fe := &dialect.FieldError{Message: msg}
	switch e.Number {
	case errNull, errTruncatedColumn, errIdentityUpdate:
		fe.Column = quoted(reColumnQ, msg, "'")
	case errComputed:
		fe.Column = quoted(reColumnDQ, msg, `"`)
	case errDupKey:
		fe.Constraint = quoted(reConstraintQ, msg, "'")
		fe.Column = first(t.ConstraintColumns(fe.Constraint))
	case errDupIndex:
		fe.Constraint = quoted(reUniqueIndex, msg, "'")
		fe.Column = first(t.ConstraintColumns(fe.Constraint))
	case errFKOrCheck:
		sm := reConstraint.FindStringSubmatch(msg)
		if sm == nil {
			return fe
		}
		fe.Constraint = strings.ReplaceAll(sm[2], `""`, `"`)
		col := quoted(reColumnQ, msg, "'")
		switch sm[1] {
		case "FOREIGN KEY":
			// The message names the referenced column; the form needs the
			// referencing one of t.
			if cols := t.ConstraintColumns(fe.Constraint); len(cols) > 0 {
				fe.Column = cols[0]
			}
		case "CHECK":
			fe.Column = col
			if fe.Column == "" {
				fe.Column = checkColumn(t, fe.Constraint)
			}
		case "REFERENCE":
			// Deleting or updating a referenced row of t: the message column
			// belongs to the referencing table, not to t.
		}
	case errIdentityInsert:
		fe.Column = identityColumn(t)
	case errTruncated, errOverflow, errOverflowConvert, errConvert, errConvertType,
		errDateString, errDateRange, errRowversion:
	default:
		return nil
	}
	return fe
}

func quoted(re *regexp.Regexp, msg, q string) string {
	sm := re.FindStringSubmatch(msg)
	if sm == nil {
		return ""
	}
	return strings.ReplaceAll(sm[1], q+q, q)
}

func identityColumn(t *meta.Table) string {
	if t == nil {
		return ""
	}
	for _, c := range t.Columns {
		if c.AutoIncrement {
			return c.Name
		}
	}
	return ""
}

// checkColumn returns the single column of t referenced by the check
// constraint's expression, or "".
func checkColumn(t *meta.Table, name string) string {
	if t == nil {
		return ""
	}
	for _, ck := range t.Checks {
		if !strings.EqualFold(ck.Name, name) {
			continue
		}
		found := ""
		for _, sm := range reBracketed.FindAllStringSubmatch(ck.Expression, -1) {
			c := t.Column(strings.ReplaceAll(sm[1], "]]", "]"))
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
	return ""
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
