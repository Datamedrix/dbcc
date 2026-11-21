package form

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"dbcc/internal/dialect"
	"dbcc/internal/dialect/mysql"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
)

func sp(s string) *string { return &s }

// itemsTable mirrors the items/parent test schema of the MySQL dialect
// (internal/dialect/mysql/testdata/schema.sql), as DescribeTable reports it.
func itemsTable() *meta.Table {
	return &meta.Table{
		Path: meta.Path{Database: "d", Object: "items"},
		Kind: meta.KindTable,
		Columns: []meta.Column{
			{Name: "id", DataType: "int unsigned", AutoIncrement: true},
			{Name: "code", DataType: "varchar(20)"},
			{Name: "region", DataType: "char(2)", Default: sp("EU")},
			{Name: "name", DataType: "varchar(200)"},
			{Name: "note", DataType: "varchar(40)", Nullable: true, Default: sp("NULL")},
			{Name: "price", DataType: "decimal(10,2)", Default: sp("0.00")},
			{Name: "qty", DataType: "int", Default: sp("1")},
			{Name: "total", DataType: "decimal(12,2)", Nullable: true, Generated: &meta.Generated{Expression: "(`price` * `qty`)", Stored: true}},
			{Name: "status", DataType: "enum('new','active','it''s done')", Default: sp("new"), EnumValues: []string{"new", "active", "it's done"}},
			{Name: "tags", DataType: "set('a','b','c')", Nullable: true, EnumValues: []string{"a", "b", "c"}},
			{Name: "created", DataType: "datetime", Default: sp("CURRENT_TIMESTAMP"), DefaultExpr: true},
			{Name: "small", DataType: "tinyint(1)", Default: sp("0")},
			{Name: "flag", DataType: "bit(1)", Nullable: true},
			{Name: "data", DataType: "blob", Nullable: true},
			{Name: "bin", DataType: "varbinary(16)", Nullable: true},
			{Name: "doc", DataType: "json", Nullable: true},
			{Name: "body", DataType: "mediumtext", Nullable: true},
			{Name: "parent_a", DataType: "int", Nullable: true},
			{Name: "parent_b", DataType: "varchar(20)", Nullable: true},
		},
		PrimaryKey:  &meta.Key{Name: "PRIMARY", Columns: []string{"id"}},
		UniqueKeys:  []meta.Key{{Name: "uq_code_region", Columns: []string{"code", "region"}}},
		ForeignKeys: []meta.ForeignKey{{Name: "fk_items_parent", Columns: []string{"parent_a", "parent_b"}, RefTable: meta.Path{Database: "d", Object: "parent"}, RefColumns: []string{"a", "b"}}},
		Checks:      []meta.Check{{Name: "chk_price", Expression: "(`price` >= 0)"}, {Name: "items_chk_1", Expression: "(`qty` < 100000)"}},
	}
}

func parentTable() *meta.Table {
	return &meta.Table{
		Path: meta.Path{Database: "d", Object: "parent"},
		Kind: meta.KindTable,
		Columns: []meta.Column{
			{Name: "a", DataType: "int"},
			{Name: "b", DataType: "varchar(20)"},
			{Name: "label", DataType: "varchar(40)", Nullable: true},
		},
		PrimaryKey: &meta.Key{Name: "PRIMARY", Columns: []string{"a", "b"}},
	}
}

// numbered is a dialect with numbered placeholders and the standard
// DEFAULT VALUES form (stands in for PostgreSQL).
type numbered struct{ *mysql.MySQL }

func (numbered) Name() string             { return "postgres" }
func (numbered) Placeholder(n int) string { return "$" + strconv.Itoa(n) }
func (numbered) InsertDefaults(t string) string {
	return "INSERT INTO " + t + " DEFAULT VALUES"
}
func (numbered) TextSearchParamCount(cols []meta.Column) int { return 1 }
func (numbered) QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
func (n numbered) Qualify(p meta.Path) string { return n.QuoteIdent(p.Object) }

var my dialect.Dialect = &mysql.MySQL{}

func TestBuildInsert(t *testing.T) {
	tbl := itemsTable()
	q, args, err := BuildInsert(my, tbl, []FieldState{
		{Column: "id", State: Value, Value: "7"}, // auto: never inserted
		{Column: "code", State: Value, Value: "c1"},
		{Column: "region", State: Untouched},
		{Column: "name", State: Value, Value: ""}, // empty string is a value
		{Column: "note", State: Null},
		{Column: "total", State: Value, Value: "5"}, // generated: never inserted
		{Column: "small", State: Value, Value: "1"},
		{Column: "flag", State: Value, Value: "0"},
		{Column: "data", State: Value, Value: "x"}, // binary: not editable
		{Column: "tags", State: Value, Value: "a,c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "INSERT INTO `d`.`items` (`code`, `name`, `note`, `small`, `flag`, `tags`) VALUES (?, ?, ?, ?, ?, ?)"
	if q != want {
		t.Fatalf("got  %s\nwant %s", q, want)
	}
	if wantArgs := []any{"c1", "", nil, true, false, "a,c"}; !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args %#v", args)
	}

	// All untouched: the dialect's all-defaults form.
	q, args, err = BuildInsert(my, tbl, []FieldState{{Column: "code"}, {Column: "id", State: Value, Value: "1"}})
	if err != nil || q != "INSERT INTO `d`.`items` () VALUES ()" || len(args) != 0 {
		t.Fatalf("defaults: %q %v %v", q, args, err)
	}
	q, _, _ = BuildInsert(numbered{&mysql.MySQL{}}, tbl, nil)
	if q != `INSERT INTO "items" DEFAULT VALUES` {
		t.Fatalf("standard defaults: %q", q)
	}

	// Numbered placeholders.
	q, args, _ = BuildInsert(numbered{&mysql.MySQL{}}, tbl, []FieldState{{Column: "code", State: Value, Value: "x"}, {Column: "note", State: Null}})
	if q != `INSERT INTO "items" ("code", "note") VALUES ($1, $2)` || !reflect.DeepEqual(args, []any{"x", nil}) {
		t.Fatalf("numbered: %q %v", q, args)
	}

	// Identifiers are quoted.
	odd := &meta.Table{Path: meta.Path{Object: "we`ird"}, Columns: []meta.Column{{Name: "co`l", DataType: "int"}}}
	q, _, _ = BuildInsert(my, odd, []FieldState{{Column: "co`l", State: Value, Value: "1"}})
	if q != "INSERT INTO `we``ird` (`co``l`) VALUES (?)" {
		t.Fatalf("quoting: %q", q)
	}

	if _, _, err := BuildInsert(my, tbl, []FieldState{{Column: "nope", State: Value}}); err == nil {
		t.Fatal("unknown column must fail")
	}
}

func TestBuildUpdate(t *testing.T) {
	tbl := itemsTable()
	fields := []FieldState{
		{Column: "id", State: Untouched, HasOrig: true, Orig: "5"},
		{Column: "code", State: Value, Value: "new", HasOrig: true, Orig: "old"},      // changed
		{Column: "region", State: Value, Value: "EU", HasOrig: true, Orig: "EU"},      // same value: skipped
		{Column: "name", State: Value, Value: "", HasOrig: true, Orig: nil},           // NULL → '' changed
		{Column: "note", State: Null, HasOrig: true, Orig: ""},                        // '' → NULL changed
		{Column: "price", State: Null, HasOrig: true, Orig: nil},                      // NULL → NULL skipped
		{Column: "qty", State: Untouched, HasOrig: true, Orig: "3"},                   // untouched skipped
		{Column: "small", State: Value, Value: "1", HasOrig: true, Orig: "1"},         // same bool
		{Column: "flag", State: Value, Value: "1", HasOrig: true, Orig: "\x01"},       // bit(1) raw byte: same
		{Column: "total", State: Value, Value: "9", HasOrig: true, Orig: "1"},         // generated: never
		{Column: "data", State: Null, HasOrig: true, Orig: exec.Binary{1}},            // binary: never
		{Column: "status", State: Value, Value: "active", HasOrig: true, Orig: "new"}, // changed
		{Column: "tags", State: Value, Value: "a"},                                    // no original: changed
	}
	q, args, err := BuildUpdate(my, tbl, fields, []KeyValue{{Column: "id", Value: "5"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE `d`.`items` SET `code` = ?, `name` = ?, `note` = ?, `status` = ?, `tags` = ? WHERE `id` = ?"
	if q != want {
		t.Fatalf("got  %s\nwant %s", q, want)
	}
	if wantArgs := []any{"new", "", nil, "active", "a", "5"}; !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args %#v", args)
	}

	// Changing a key column: WHERE uses the original key, composite.
	q, args, err = BuildUpdate(my, tbl, []FieldState{
		{Column: "code", State: Value, Value: "c9", HasOrig: true, Orig: "c1"},
		{Column: "region", State: Value, Value: "US", HasOrig: true, Orig: "EU"},
	}, []KeyValue{{Column: "code", Value: "c1"}, {Column: "region", Value: "EU"}})
	if err != nil {
		t.Fatal(err)
	}
	if q != "UPDATE `d`.`items` SET `code` = ?, `region` = ? WHERE `code` = ? AND `region` = ?" ||
		!reflect.DeepEqual(args, []any{"c9", "US", "c1", "EU"}) {
		t.Fatalf("composite: %s %v", q, args)
	}

	// Binary key values are bound as []byte.
	_, args, _ = BuildUpdate(my, tbl, []FieldState{{Column: "code", State: Value, Value: "x"}}, []KeyValue{{Column: "bin", Value: exec.Binary{0xff}}})
	if b, ok := args[1].([]byte); !ok || len(b) != 1 || b[0] != 0xff {
		t.Fatalf("binary key arg %#v", args[1])
	}

	// Numbered placeholders continue into WHERE.
	q, _, _ = BuildUpdate(numbered{&mysql.MySQL{}}, tbl, []FieldState{{Column: "code", State: Value, Value: "x", HasOrig: true, Orig: "y"}}, []KeyValue{{Column: "id", Value: "1"}})
	if q != `UPDATE "items" SET "code" = $1 WHERE "id" = $2` {
		t.Fatalf("numbered: %s", q)
	}

	if _, _, err := BuildUpdate(my, tbl, fields[:1], []KeyValue{{Column: "id", Value: "5"}}); !errors.Is(err, ErrNoChanges) {
		t.Fatalf("no changes: %v", err)
	}
	if _, _, err := BuildUpdate(my, tbl, fields, nil); err == nil {
		t.Fatal("missing key must fail")
	}
	if _, _, err := BuildUpdate(my, tbl, fields, []KeyValue{{Column: "id", Value: nil}}); err == nil {
		t.Fatal("NULL key must fail")
	}
}

func TestCanEdit(t *testing.T) {
	tbl := itemsTable()
	cols := func(names ...string) []exec.ColumnInfo {
		var out []exec.ColumnInfo
		for _, n := range names {
			out = append(out, exec.ColumnInfo{Name: n})
		}
		return out
	}
	// Primary key present.
	if k, r := CanEdit(tbl, cols("ID", "code"), exec.Row{"1", "c"}); r != "" || !reflect.DeepEqual(k, []string{"id"}) {
		t.Fatalf("pk: %v %q", k, r)
	}
	// Primary key not in the result: unique key.
	if k, r := CanEdit(tbl, cols("code", "region"), exec.Row{"c", "EU"}); r != "" || !reflect.DeepEqual(k, []string{"code", "region"}) {
		t.Fatalf("uk: %v %q", k, r)
	}
	// Unique key with a NULL column and no primary key column.
	if k, r := CanEdit(tbl, cols("code", "region"), exec.Row{"c", nil}); k != nil || !strings.Contains(r, "region is NULL") || !strings.Contains(r, "column id is not in the result") {
		t.Fatalf("uk null: %v %q", k, r)
	}
	// Primary key NULL in the row (outer-join style result).
	if _, r := CanEdit(tbl, cols("id"), exec.Row{nil}); !strings.Contains(r, "id is NULL") {
		t.Fatalf("pk null: %q", r)
	}
	// No keys at all.
	nokey := &meta.Table{Path: meta.Path{Object: "logs"}, Columns: []meta.Column{{Name: "msg"}}}
	if _, r := CanEdit(nokey, cols("msg"), exec.Row{"x"}); !strings.Contains(r, "no primary key or unique key") {
		t.Fatalf("no key: %q", r)
	}
	if _, r := CanEdit(&meta.Table{Kind: meta.KindView, Path: meta.Path{Object: "v"}}, nil, nil); !strings.Contains(r, "view") {
		t.Fatalf("view: %q", r)
	}
	if _, r := CanEdit(nil, nil, nil); r == "" {
		t.Fatal("nil table")
	}
}

func TestFieldKinds(t *testing.T) {
	tbl := itemsTable()
	want := map[string]Kind{
		"id": KindAuto, "code": KindText, "region": KindText, "name": KindText, "note": KindText,
		"price": KindText, "qty": KindText, "total": KindAuto, "status": KindEnum, "tags": KindSet,
		"created": KindText, "small": KindBool, "flag": KindBool, "data": KindBinary, "bin": KindBinary,
		"doc": KindLongText, "body": KindLongText, "parent_a": KindFK, "parent_b": KindFK,
	}
	for _, c := range tbl.Columns {
		if got := FieldKind(tbl, c); got != want[c.Name] {
			t.Errorf("%s (%s): %s, want %s", c.Name, c.DataType, got, want[c.Name])
		}
	}
	for dt, k := range map[string]Kind{
		"boolean": KindBool, "bool": KindBool, "bit": KindBool, "bit(8)": KindBinary, "tinyint(4)": KindText,
		"tinyint(1) unsigned": KindBool, "bytea": KindBinary, "jsonb": KindLongText, "text": KindLongText,
		"character varying(20)": KindText, "geometry": KindBinary, "longblob": KindBinary,
	} {
		if got := FieldKind(&meta.Table{}, meta.Column{Name: "x", DataType: dt}); got != k {
			t.Errorf("%s: %s, want %s", dt, got, k)
		}
	}
	f := New(Options{Table: tbl})
	m := map[string]string{}
	for _, fi := range f.Fields() {
		m[fi.Column] = fi.Markers
	}
	for col, w := range map[string]string{
		"id": "PK NN    DF", "code": "UK NN      ", "region": "UK NN    DF", "note": "         DF",
		"parent_a": "      FK   ", "status": "   NN    DF",
	} {
		if m[col] != w {
			t.Errorf("markers %s: %q want %q", col, m[col], w)
		}
	}
}
