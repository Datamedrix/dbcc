// Package form is the row form (spec 11): one form for inserting a new row
// and for editing an existing record of a table, with NULL/untouched field
// states, a foreign-key lookup window, a multi-line editor for long text,
// statement generation with bound parameters and server-side validation
// (errors are mapped back to fields through Dialect.MapError).
package form

import (
	"strconv"
	"strings"

	"dbcc/internal/meta"
)

// Kind is the field kind of a column (spec 11 table).
type Kind int

const (
	KindText     Kind = iota // plain text input
	KindAuto                 // auto-increment, identity or generated: read-only "auto"
	KindEnum                 // drop-down
	KindSet                  // check boxes, one per member
	KindBool                 // one check box
	KindFK                   // read-only key display; F4 opens the lookup window
	KindLongText             // TEXT/JSON: one-line preview; F4 opens the multi-line editor
	KindBinary               // BLOB and other binary: not editable in v1
)

func (k Kind) String() string {
	switch k {
	case KindAuto:
		return "auto"
	case KindEnum:
		return "enum"
	case KindSet:
		return "set"
	case KindBool:
		return "bool"
	case KindFK:
		return "fk"
	case KindLongText:
		return "longtext"
	case KindBinary:
		return "binary"
	}
	return "text"
}

// Editable reports whether the field can take a value in the form.
func (k Kind) Editable() bool { return k != KindAuto && k != KindBinary }

// baseType is the lower-case type name without length, precision or
// modifiers: "varchar(20)" → "varchar", "int unsigned" → "int",
// "character varying(20)" → "character varying".
func baseType(dataType string) string {
	t := strings.ToLower(strings.TrimSpace(dataType))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSpace(t)
	for _, mod := range []string{" unsigned", " zerofill", " signed"} {
		t = strings.TrimSuffix(t, mod)
	}
	return strings.TrimSpace(t)
}

// typeArg returns the first argument of a type: "bit(1)" → "1".
func typeArg(dataType string) string {
	i := strings.IndexByte(dataType, '(')
	if i < 0 {
		return ""
	}
	j := strings.IndexAny(dataType[i:], ",)")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(dataType[i+1 : i+j])
}

// IsBoolType reports boolean columns: bool, boolean, tinyint(1), bit(1)
// and bit without length.
func IsBoolType(dataType string) bool {
	switch baseType(dataType) {
	case "bool", "boolean":
		return true
	case "tinyint":
		return typeArg(dataType) == "1"
	case "bit":
		a := typeArg(dataType)
		return a == "" || a == "1"
	}
	return false
}

// IsBinaryType reports binary, spatial and other columns the form cannot
// edit as text.
func IsBinaryType(dataType string) bool {
	switch baseType(dataType) {
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bytea", "image",
		"bit", "varbit", "bit varying", "geometry", "geography", "point", "linestring", "polygon",
		"multipoint", "multilinestring", "multipolygon", "geometrycollection", "geomcollection",
		"vector", "hierarchyid", "rowversion":
		return true
	}
	return false
}

// IsLongTextType reports TEXT and JSON-like columns that get a preview and
// the multi-line editor.
func IsLongTextType(dataType string) bool {
	switch baseType(dataType) {
	case "text", "tinytext", "mediumtext", "longtext", "ntext", "json", "jsonb", "xml", "clob", "nclob", "citext":
		return true
	}
	return false
}

// IsTextType reports character columns (used to pick the "first text
// column" of a referenced row).
func IsTextType(dataType string) bool {
	b := baseType(dataType)
	return strings.Contains(b, "char") || strings.Contains(b, "text") || b == "string"
}

// FieldKind returns the field kind of column c of table t (spec 11).
func FieldKind(t *meta.Table, c meta.Column) Kind {
	switch {
	case c.AutoIncrement || c.Generated != nil:
		return KindAuto
	case t != nil && fkOf(t, c.Name) != nil:
		return KindFK
	case baseType(c.DataType) == "set":
		return KindSet
	case len(c.EnumValues) > 0:
		return KindEnum
	case IsBoolType(c.DataType):
		return KindBool
	case IsBinaryType(c.DataType):
		return KindBinary
	case IsLongTextType(c.DataType):
		return KindLongText
	}
	return KindText
}

// fkOf returns the first foreign key of t that contains column col.
func fkOf(t *meta.Table, col string) *meta.ForeignKey {
	for i := range t.ForeignKeys {
		for _, c := range t.ForeignKeys[i].Columns {
			if strings.EqualFold(c, col) {
				return &t.ForeignKeys[i]
			}
		}
	}
	return nil
}

// isKeyColumn reports whether col belongs to the primary key or a unique key.
func isKeyColumn(t *meta.Table, col string) (primary, unique bool) {
	if t.PrimaryKey != nil && containsFold(t.PrimaryKey.Columns, col) {
		primary = true
	}
	for _, k := range t.UniqueKeys {
		if containsFold(k.Columns, col) {
			unique = true
		}
	}
	return
}

func containsFold(list []string, s string) bool {
	return indexFold(list, s) >= 0
}

func indexFold(list []string, s string) int {
	for i, x := range list {
		if strings.EqualFold(x, s) {
			return i
		}
	}
	return -1
}

// parseBool interprets a stored boolean value ("1", "0", "\x01", "true", "t").
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "\x01", "true", "t", "y", "yes", "on":
		return true
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
		return n != 0
	}
	return false
}

// splitSet splits a SET value "a,b" into its members.
func splitSet(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
