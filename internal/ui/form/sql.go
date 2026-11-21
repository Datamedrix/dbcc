package form

import (
	"errors"
	"fmt"
	"strings"

	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
)

// State is the state of a field (spec 11).
type State int

const (
	Untouched State = iota // not set: omitted from INSERT, unchanged in UPDATE
	Value                  // a value, including the empty string
	Null                   // explicitly NULL
)

func (s State) String() string {
	switch s {
	case Value:
		return "value"
	case Null:
		return "NULL"
	}
	return "untouched"
}

// FieldState is the input of BuildInsert and BuildUpdate: the state of the
// field of one column.
type FieldState struct {
	Column string
	State  State
	Value  string // State == Value
	// HasOrig and Orig hold the original value of an edited record
	// (nil = NULL, string or exec.Binary). BuildUpdate leaves out fields
	// whose new value equals the original.
	HasOrig bool
	Orig    any
}

// KeyValue is one column of the original key of an edited record.
type KeyValue struct {
	Column string
	Value  any // as read: string or exec.Binary (never nil)
}

// ErrNoChanges is returned by BuildUpdate when no field changed.
var ErrNoChanges = errors.New("no changes")

func placeholder(d dialect.Dialect, n int) string { return d.Placeholder(n) }

// arg converts a field value into a bind parameter: booleans become Go
// bools (the drivers send them as 1/0 or true/false, which bit(1) accepts,
// unlike the string "1"); everything else is passed as text and converted
// by the server.
func arg(kind Kind, value string) any {
	if kind == KindBool {
		return parseBool(value)
	}
	return value
}

// keyArg converts an original key value into a bind parameter.
func keyArg(v any) any {
	if b, ok := v.(exec.Binary); ok {
		return []byte(b)
	}
	return v
}

// column resolves a field's column in t.
func column(t *meta.Table, name string) (*meta.Column, error) {
	c := t.Column(name)
	if c == nil {
		return nil, fmt.Errorf("column %q not in table %s", name, t.Path.Object)
	}
	return c, nil
}

// BuildInsert returns the INSERT for a new row: only fields that are not
// untouched appear in the column list, so the database applies its
// defaults to the others. Read-only kinds (auto, binary) are never
// inserted. With nothing to insert the dialect's all-defaults form is used.
func BuildInsert(d dialect.Dialect, t *meta.Table, fields []FieldState) (string, []any, error) {
	if t == nil {
		return "", nil, errors.New("no table metadata")
	}
	table := d.Qualify(t.Path)
	var cols, ph []string
	var args []any
	for _, f := range fields {
		if f.State == Untouched {
			continue
		}
		c, err := column(t, f.Column)
		if err != nil {
			return "", nil, err
		}
		kind := FieldKind(t, *c)
		if !kind.Editable() {
			continue
		}
		cols = append(cols, d.QuoteIdent(c.Name))
		if f.State == Null {
			args = append(args, nil)
		} else {
			args = append(args, arg(kind, f.Value))
		}
		ph = append(ph, placeholder(d, len(args)))
	}
	if len(cols) == 0 {
		return d.InsertDefaults(table), nil, nil
	}
	return "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(ph, ", ") + ")", args, nil
}

// changed reports whether a field differs from its original value.
func changed(kind Kind, f FieldState) bool {
	switch f.State {
	case Untouched:
		return false
	case Null:
		return !f.HasOrig || f.Orig != nil
	}
	if !f.HasOrig {
		return true
	}
	s, ok := f.Orig.(string)
	if !ok {
		return true
	}
	if kind == KindBool {
		return parseBool(s) != parseBool(f.Value)
	}
	return s != f.Value
}

// BuildUpdate returns the UPDATE for an edited record: only changed fields
// appear in SET, and WHERE matches the ORIGINAL key values. It returns
// ErrNoChanges when nothing changed.
func BuildUpdate(d dialect.Dialect, t *meta.Table, fields []FieldState, key []KeyValue) (string, []any, error) {
	if t == nil {
		return "", nil, errors.New("no table metadata")
	}
	if len(key) == 0 {
		return "", nil, errors.New("no key to identify the record")
	}
	var set []string
	var args []any
	for _, f := range fields {
		c, err := column(t, f.Column)
		if err != nil {
			return "", nil, err
		}
		kind := FieldKind(t, *c)
		if !kind.Editable() || !changed(kind, f) {
			continue
		}
		if f.State == Null {
			args = append(args, nil)
		} else {
			args = append(args, arg(kind, f.Value))
		}
		set = append(set, d.QuoteIdent(c.Name)+" = "+placeholder(d, len(args)))
	}
	if len(set) == 0 {
		return "", nil, ErrNoChanges
	}
	var where []string
	for _, k := range key {
		c, err := column(t, k.Column)
		if err != nil {
			return "", nil, err
		}
		if k.Value == nil {
			return "", nil, fmt.Errorf("key column %s is NULL", c.Name)
		}
		args = append(args, keyArg(k.Value))
		where = append(where, d.QuoteIdent(c.Name)+" = "+placeholder(d, len(args)))
	}
	return "UPDATE " + d.Qualify(t.Path) + " SET " + strings.Join(set, ", ") + " WHERE " + strings.Join(where, " AND "), args, nil
}

// CanEdit reports whether a record of a table-origin result can be edited
// (spec 11): the table needs a primary key or a unique key whose columns
// are all present in the result and NOT NULL in the row. It returns the
// key columns to identify the record by, or a reason why editing is not
// possible.
func CanEdit(t *meta.Table, cols []exec.ColumnInfo, row exec.Row) (keyCols []string, reason string) {
	if t == nil {
		return nil, "no table metadata for this result"
	}
	if t.Kind == meta.KindView {
		return nil, fmt.Sprintf("%s is a view; only tables can be edited", t.Path.Object)
	}
	var keys []meta.Key
	if t.PrimaryKey != nil && len(t.PrimaryKey.Columns) > 0 {
		keys = append(keys, *t.PrimaryKey)
	}
	for _, k := range t.UniqueKeys {
		if len(k.Columns) > 0 {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Sprintf("table %s has no primary key or unique key, so a record cannot be identified", t.Path.Object)
	}
	var why []string
	for _, k := range keys {
		ok := true
		for _, c := range k.Columns {
			i := resultIndex(cols, c)
			switch {
			case i < 0 || i >= len(row):
				why = append(why, fmt.Sprintf("%s: column %s is not in the result", keyLabel(t, k), c))
				ok = false
			case row[i] == nil:
				why = append(why, fmt.Sprintf("%s: column %s is NULL in this row", keyLabel(t, k), c))
				ok = false
			}
			if !ok {
				break
			}
		}
		if ok {
			return k.Columns, ""
		}
	}
	return nil, "no primary or unique key with all columns present and NOT NULL (" + strings.Join(why, "; ") + ")"
}

func keyLabel(t *meta.Table, k meta.Key) string {
	if t.PrimaryKey != nil && k.Name == t.PrimaryKey.Name && strings.Join(k.Columns, ",") == strings.Join(t.PrimaryKey.Columns, ",") {
		return "primary key"
	}
	return "unique key " + k.Name
}

// resultIndex returns the index of column name in a result (case-insensitive).
func resultIndex(cols []exec.ColumnInfo, name string) int {
	for i, c := range cols {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}
