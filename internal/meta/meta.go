// Package meta holds the metadata model that feeds cat, completion, forms,
// cp -a and the grid (spec section 6.2), plus a lazy per-schema cache.
package meta

import (
	"errors"
	"strings"
)

// ErrNotFound is wrapped by Dialect.DescribeTable and ObjectSource when the
// object does not exist.
var ErrNotFound = errors.New("object not found")

// ObjectKind is the kind of a database object.
type ObjectKind string

const (
	KindDatabase         ObjectKind = "database"
	KindSchema           ObjectKind = "schema"
	KindTable            ObjectKind = "table"
	KindView             ObjectKind = "view"
	KindMaterializedView ObjectKind = "materialized view"
	KindProcedure        ObjectKind = "procedure"
	KindFunction         ObjectKind = "function"
	KindTrigger          ObjectKind = "trigger"
	KindEvent            ObjectKind = "event"
	KindSequence         ObjectKind = "sequence"
)

// Path addresses a connection, database, schema and optionally an object.
// MySQL and MariaDB have no schema level (Schema stays empty).
type Path struct {
	Conn     string
	Database string
	Schema   string
	Object   string
}

// String renders the navigation part as /conn/db/schema.
func (p Path) String() string {
	parts := []string{}
	for _, s := range []string{p.Conn, p.Database, p.Schema} {
		if s == "" {
			break
		}
		parts = append(parts, s)
	}
	return "/" + strings.Join(parts, "/")
}

// Object is an entry returned by Dialect.ListObjects.
type Object struct {
	Name    string
	Kind    ObjectKind
	Comment string
	System  bool // system database/schema (listed last, dimmed)
}

type Table struct {
	Path        Path
	Kind        ObjectKind // table or view
	Columns     []Column
	PrimaryKey  *Key
	UniqueKeys  []Key
	ForeignKeys []ForeignKey
	Checks      []Check
	Indexes     []Index
	Triggers    []Trigger
	Comment     string
	Options     map[string]string // engine, charset, collation, rows, partitioning
	// ViewDefinition is the SELECT text of a view (Kind == KindView) as the
	// catalog reports it; empty for tables. Used by cp -a on views (A2).
	ViewDefinition string
}

type Column struct {
	Name     string
	Position int
	DataType string // as declared: varchar(40), decimal(10,2) unsigned
	Nullable bool
	Default  *string // nil means no default (or DEFAULT NULL)
	// DefaultExpr reports that Default is an SQL expression such as
	// CURRENT_TIMESTAMP or uuid() rather than a literal value.
	DefaultExpr   bool
	AutoIncrement bool
	Generated     *Generated
	OnUpdate      string // e.g. CURRENT_TIMESTAMP (MySQL)
	EnumValues    []string
	Collation     string
	Comment       string
}

type Generated struct {
	Expression string
	Stored     bool
}

type Key struct {
	Name    string
	Columns []string
}

type ForeignKey struct {
	Name       string
	Columns    []string
	RefTable   Path
	RefColumns []string
	OnUpdate   string
	OnDelete   string
}

type Check struct {
	Name        string
	Expression  string
	NotEnforced bool // MySQL CHECK … NOT ENFORCED
}

type IndexColumn struct {
	Name       string // column name or expression
	Descending bool
	SubPart    int // prefix length, 0 = whole column
	Expression bool
}

type Index struct {
	Name    string
	Columns []IndexColumn
	Unique  bool
	Type    string // BTREE, HASH, FULLTEXT, SPATIAL, gin, …
	// Invisible marks indexes the optimizer ignores (MySQL INVISIBLE,
	// MariaDB IGNORED).
	Invisible bool
	Comment   string
	// Include lists the non-key columns stored in the index
	// (PostgreSQL and MSSQL INCLUDE (…)).
	Include []string
	// Filter is the predicate of a partial (PostgreSQL) or filtered
	// (MSSQL) index, without WHERE; "" for a full index.
	Filter string
}

type Trigger struct {
	Name   string
	Timing string // BEFORE, AFTER, INSTEAD OF
	Event  string // INSERT, UPDATE, DELETE
}

// Column returns the column with the given name (case-insensitive) or nil.
func (t *Table) Column(name string) *Column {
	for i := range t.Columns {
		if strings.EqualFold(t.Columns[i].Name, name) {
			return &t.Columns[i]
		}
	}
	return nil
}

// ConstraintColumns returns the columns of the primary key, unique key,
// foreign key or index with the given name (case-insensitive), or nil.
// The name "PRIMARY" also matches an unnamed primary key.
func (t *Table) ConstraintColumns(name string) []string {
	if t == nil || name == "" {
		return nil
	}
	if pk := t.PrimaryKey; pk != nil && (strings.EqualFold(pk.Name, name) || strings.EqualFold(name, "PRIMARY")) {
		return pk.Columns
	}
	for _, k := range t.UniqueKeys {
		if strings.EqualFold(k.Name, name) {
			return k.Columns
		}
	}
	for _, fk := range t.ForeignKeys {
		if strings.EqualFold(fk.Name, name) {
			return fk.Columns
		}
	}
	for _, ix := range t.Indexes {
		if strings.EqualFold(ix.Name, name) {
			var cols []string
			for _, c := range ix.Columns {
				if !c.Expression {
					cols = append(cols, c.Name)
				}
			}
			return cols
		}
	}
	return nil
}
