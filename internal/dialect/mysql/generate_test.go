package mysql

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	driver "github.com/go-sql-driver/mysql"

	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

func TestEnumValues(t *testing.T) {
	got := enumValues(`enum('new','active','it''s done','a,b','x\\y')`)
	want := []string{"new", "active", "it's done", "a,b", `x\y`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if got := enumValues("set('a','b')"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("set: %q", got)
	}
	if enumValues("varchar(10)") != nil {
		t.Fatal("varchar has no values")
	}
}

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func TestNormalizeDefault(t *testing.T) {
	type tc struct {
		def     sql.NullString
		gen     bool
		want    *string
		wantExp bool
	}
	str := func(s string) *string { return &s }
	my := &MySQL{}
	ma := &MySQL{MariaDB: true}
	for i, c := range []struct {
		m *MySQL
		tc
	}{
		{my, tc{sql.NullString{}, false, nil, false}},
		{my, tc{ns("NULL"), false, str("NULL"), false}}, // MySQL: string 'NULL'
		{my, tc{ns("it's"), false, str("it's"), false}},
		{my, tc{ns("CURRENT_TIMESTAMP"), true, str("CURRENT_TIMESTAMP"), true}},
		{my, tc{ns(`concat(_utf8mb4\'a\\\'b\')`), true, str(`concat(_utf8mb4'a\'b')`), true}},
		{ma, tc{sql.NullString{}, false, nil, false}},
		{ma, tc{ns("NULL"), false, nil, false}}, // MariaDB: DEFAULT NULL
		{ma, tc{ns("'NULL'"), false, str("NULL"), false}},
		{ma, tc{ns("'it''s'"), false, str("it's"), false}},
		{ma, tc{ns(`'b\\s'`), false, str(`b\s`), false}},
		{ma, tc{ns("''"), false, str(""), false}},
		{ma, tc{ns("-1.5"), false, str("-1.5"), false}},
		{ma, tc{ns("current_timestamp()"), false, str("current_timestamp()"), true}},
		{ma, tc{ns("uuid()"), false, str("uuid()"), true}},
	} {
		got, exp := c.m.normalizeDefault(c.def, c.gen)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) || exp != c.wantExp {
			g := "<nil>"
			if got != nil {
				g = *got
			}
			t.Errorf("%d: %v → %q expr=%v", i, c.def, g, exp)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, c := range []struct {
		v    string
		want bool
	}{{"8.4.10", true}, {"8.0.16", true}, {"8.0.15-log", false}, {"5.7.44", false}, {"", true}} {
		if got := (&MySQL{Version: c.v}).versionAtLeast(8, 0, 16); got != c.want {
			t.Errorf("%s: %v", c.v, got)
		}
	}
	if !(&MySQL{Version: "11.8.9-MariaDB-ubu2404"}).versionAtLeast(10, 6, 0) {
		t.Error("mariadb 11")
	}
}

func TestChooseDelimiter(t *testing.T) {
	if d := chooseDelimiter("BEGIN SELECT 1; END"); d != "$$" {
		t.Fatal(d)
	}
	if d := chooseDelimiter("BEGIN SET @a='$$'; END"); d != "//" {
		t.Fatal(d)
	}
	if d := chooseDelimiter("$$ // ;; $$$ /// @@@@"); d != "$$1$$" {
		t.Fatal(d)
	}
}

func TestDefiner(t *testing.T) {
	src := "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`%` SQL SECURITY DEFINER VIEW `v` AS select 1"
	d, ok := findDefiner(src)
	if !ok || d != "`root`@`%`" {
		t.Fatalf("%q %v", d, ok)
	}
	if got := removeDefiner(src); got != "CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 1" {
		t.Fatal(got)
	}
	if accountKey("`root`@`%`") != "root@%" || accountKey("'a''b'@'h'") != "a'b@h" {
		t.Fatal("accountKey")
	}
	if _, ok := findDefiner("CREATE PROCEDURE p() BEGIN END"); ok {
		t.Fatal("no definer")
	}
	for g, want := range map[string]bool{
		"GRANT ALL PRIVILEGES ON *.* TO `root`@`%`":            true,
		"GRANT SELECT, SUPER ON *.* TO `x`@`%`":                true,
		"GRANT SET_ANY_DEFINER,XA_RECOVER_ADMIN ON *.* TO `x`": true,
		"GRANT ALL PRIVILEGES ON `db`.* TO `x`@`%`":            false,
		"GRANT USAGE ON *.* TO `x`@`%`":                        false,
		"GRANT SET USER ON *.* TO `x`@`%`":                     true,
		"GRANT EXECUTE ON PROCEDURE `db`.`p` TO `x`@`%`":       false,
	} {
		if hasDefinerPrivilege(g) != want {
			t.Errorf("%s: want %v", g, want)
		}
	}
}

func TestQualifyCreate(t *testing.T) {
	m := &MySQL{}
	obj := meta.Path{Database: "d b", Object: "trg"}
	got, err := m.qualifyCreate("CREATE DEFINER=`root`@`%` TRIGGER trg BEFORE INSERT ON items FOR EACH ROW SET NEW.a = 1", "TRIGGER", obj, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "CREATE DEFINER=`root`@`%` TRIGGER `d b`.`trg` BEFORE INSERT ON `d b`.`items` FOR EACH ROW SET NEW.a = 1"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	got, _ = m.qualifyCreate("CREATE DEFINER=`root`@`%` PROCEDURE `x`.`p`() BEGIN END", "PROCEDURE", obj, true)
	if got != "CREATE OR REPLACE DEFINER=`root`@`%` PROCEDURE `x`.`p`() BEGIN END" {
		t.Fatal(got)
	}
	got, _ = m.qualifyCreate("CREATE TRIGGER IF NOT EXISTS t1 AFTER DELETE ON t FOR EACH ROW DO 1", "TRIGGER", obj, true)
	if !strings.Contains(got, "TRIGGER  `d b`.`t1` AFTER DELETE ON `d b`.`t`") || !strings.HasPrefix(got, "CREATE OR REPLACE ") {
		t.Fatal(got)
	}
}

// TestReplaceScriptShape checks that the script layout produced for
// routines splits into the expected statements with the project splitter.
func TestReplaceScriptShape(t *testing.T) {
	body := "CREATE DEFINER=`root`@`%` PROCEDURE `db`.`p`()\nBEGIN\n  SET @x = '$$';\n  SELECT 1;\nEND"
	d := chooseDelimiter(body)
	script := fmt.Sprintf("DROP PROCEDURE IF EXISTS `db`.`p`;\nDELIMITER %s\n%s%s\nDELIMITER ;\n", d, body, d)
	stmts, err := sqltext.NewMySQL().Split(script)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 2 || stmts[0].Text != "DROP PROCEDURE IF EXISTS `db`.`p`" || stmts[1].Text != body {
		t.Fatalf("%#v", stmts)
	}
	if !endsWithLineComment("SELECT 1 -- x") || endsWithLineComment("SELECT 1 /* x */") || endsWithLineComment("END") {
		t.Fatal("endsWithLineComment")
	}
}

func TestCopyTableSQL(t *testing.T) {
	m := &MySQL{}
	src := meta.Path{Database: "d", Object: "items"}
	dst := meta.Path{Database: "d", Object: "items2"}
	got, _ := m.CopyTable(src, dst, nil, false)
	if !reflect.DeepEqual(got, []string{"CREATE TABLE `d`.`items2` AS SELECT * FROM `d`.`items`"}) {
		t.Fatal(got)
	}
	tbl := &meta.Table{Kind: meta.KindTable, Columns: []meta.Column{
		{Name: "id"}, {Name: "total", Generated: &meta.Generated{Expression: "a*b", Stored: true}}, {Name: "n`x"},
	}}
	got, _ = m.CopyTable(src, dst, tbl, true)
	want := []string{
		"CREATE TABLE `d`.`items2` LIKE `d`.`items`",
		"INSERT INTO `d`.`items2` (`id`, `n``x`) SELECT `id`, `n``x` FROM `d`.`items`",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	view := &meta.Table{Kind: meta.KindView, ViewDefinition: "select 1 AS `a`", Options: map[string]string{"check_option": "CASCADED"}}
	got, _ = m.CopyTable(src, dst, view, true)
	if !reflect.DeepEqual(got, []string{"CREATE VIEW `d`.`items2` AS select 1 AS `a` WITH CASCADED CHECK OPTION"}) {
		t.Fatal(got)
	}
	if _, err := m.CopyTable(src, dst, nil, true); err == nil {
		t.Fatal("structure copy without metadata must fail")
	}
}

func TestTextSearchPredicate(t *testing.T) {
	m := &MySQL{}
	cols := []meta.Column{
		{Name: "name", DataType: "varchar(20)"},
		{Name: "price", DataType: "decimal(10,2)"},
		{Name: "data", DataType: "blob"},
		{Name: "bin", DataType: "varbinary(16)"},
		{Name: "status", DataType: "enum('a','b')"},
	}
	got := m.TextSearchPredicate(cols, "?")
	want := "(LOCATE(LOWER(?), LOWER(`name`)) > 0 OR LOCATE(LOWER(?), LOWER(CAST(`price` AS CHAR))) > 0 OR LOCATE(LOWER(?), LOWER(`status`)) > 0)"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if n := len(m.TextSearchColumns(cols)); n != strings.Count(got, "?") {
		t.Fatalf("param count %d", n)
	}
	if m.TextSearchPredicate(cols[2:4], "") != "1 = 0" {
		t.Fatal("no searchable columns")
	}
}

func TestMapErrorMessages(t *testing.T) {
	m := &MySQL{}
	tbl := &meta.Table{
		Path:       meta.Path{Database: "d", Object: "items"},
		PrimaryKey: &meta.Key{Name: "PRIMARY", Columns: []string{"id"}},
		UniqueKeys: []meta.Key{{Name: "uq_code_region", Columns: []string{"code", "region"}}},
		Columns:    []meta.Column{{Name: "id"}, {Name: "code"}, {Name: "region"}, {Name: "price"}, {Name: "qty"}},
		Checks:     []meta.Check{{Name: "chk_price", Expression: "(`price` >= 0)"}, {Name: "chk_two", Expression: "`price` < `qty`"}},
	}
	for _, c := range []struct {
		num        uint16
		msg        string
		col, cname string
	}{
		{1048, "Column 'code' cannot be null", "code", ""},
		{1364, "Field 'code' doesn't have a default value", "code", ""},
		{1406, "Data too long for column 'region' at row 1", "region", ""},
		{1366, "Incorrect integer value: 'x' for column `d`.`items`.`qty` at row 1", "qty", ""},
		{1062, "Duplicate entry 'a-EU' for key 'items.uq_code_region'", "code", "uq_code_region"},
		{1062, "Duplicate entry '1' for key 'PRIMARY'", "id", "PRIMARY"},
		{1452, "Cannot add or update a child row: a foreign key constraint fails (`d`.`items`, CONSTRAINT `fk_p` FOREIGN KEY (`pa`, `pb`) REFERENCES `parent` (`a`, `b`) ON DELETE CASCADE)", "pa", "fk_p"},
		{1451, "Cannot delete or update a parent row: a foreign key constraint fails (`d`.`items`, CONSTRAINT `fk_p` FOREIGN KEY (`pa`, `pb`) REFERENCES `parent` (`a`, `b`) ON DELETE CASCADE)", "a", "fk_p"},
		{3819, "Check constraint 'chk_price' is violated.", "price", "chk_price"},
		{4025, "CONSTRAINT `chk_two` failed for `d`.`items`", "", "chk_two"},
		{4025, "CONSTRAINT `qty` failed for `d`.`items`", "qty", "qty"},
	} {
		fe := m.MapError(fmt.Errorf("wrapped: %w", &driver.MySQLError{Number: c.num, Message: c.msg}), tbl)
		if fe == nil || fe.Column != c.col || fe.Constraint != c.cname {
			t.Errorf("%d %s: %+v", c.num, c.msg, fe)
		}
	}
	if m.MapError(fmt.Errorf("plain"), tbl) != nil || m.MapError(&driver.MySQLError{Number: 1064}, tbl) != nil {
		t.Error("unrelated errors must map to nil")
	}
}

func TestKeywordsFunctions(t *testing.T) {
	for _, d := range []*MySQL{{}, {MariaDB: true}} {
		kw, fn := d.Keywords(), d.Functions()
		if len(kw) < 300 || len(fn) < 200 {
			t.Fatalf("%s: %d keywords, %d functions", d.Name(), len(kw), len(fn))
		}
		for i := 1; i < len(kw); i++ {
			if kw[i] <= kw[i-1] {
				t.Fatalf("not sorted/unique at %s", kw[i])
			}
		}
	}
	has := func(l []string, w string) bool {
		for _, x := range l {
			if x == w {
				return true
			}
		}
		return false
	}
	if !has((&MySQL{MariaDB: true}).Keywords(), "RETURNING") || has((&MySQL{}).Keywords(), "RETURNING") {
		t.Fatal("RETURNING is MariaDB-only")
	}
}
