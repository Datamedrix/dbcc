package sqltext

import (
	"reflect"
	"strings"
	"testing"
)

// ansiTok is a minimal tokenizer for tests with ANSI/MSSQL quoting: "…"
// and […] are quoted identifiers, '…' strings, -- and /* */ comments,
// ";" the delimiter. The real PostgreSQL and MSSQL tokenizers live
// elsewhere; the completion code only uses the Tokenizer interface.
type ansiTok struct{}

func (ansiTok) Tokenize(src string) []Token {
	var out []Token
	i := 0
	emit := func(k TokenKind, end int) {
		out = append(out, Token{Kind: k, Text: src[i:end], Start: i, End: end})
		i = end
	}
	find := func(from int, s string) int {
		if k := strings.Index(src[from:], s); k >= 0 {
			return from + k + len(s)
		}
		return len(src)
	}
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\n' || c == '\t' || c == '\r':
			j := i
			for j < len(src) && strings.IndexByte(" \n\t\r", src[j]) >= 0 {
				j++
			}
			emit(TokWhitespace, j)
		case strings.HasPrefix(src[i:], "--"):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				emit(TokComment, len(src))
			} else {
				emit(TokComment, i+j)
			}
		case strings.HasPrefix(src[i:], "/*"):
			emit(TokComment, find(i+2, "*/"))
		case c == '\'':
			emit(TokString, find(i+1, "'"))
		case c == '"':
			emit(TokQuotedIdent, myScanQuoted(src, i, '"', false))
		case c == '[':
			j := i + 1
			for j < len(src) {
				if src[j] == ']' {
					if j+1 < len(src) && src[j+1] == ']' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			emit(TokQuotedIdent, j)
		case c == ';':
			emit(TokDelimiter, i+1)
		case myIsDigit(c):
			j := i
			for j < len(src) && myIsDigit(src[j]) {
				j++
			}
			emit(TokNumber, j)
		case myIsIdent(c) || c == '@':
			j := i + 1
			for j < len(src) && myIsIdent(src[j]) {
				j++
			}
			emit(TokIdent, j)
		default:
			emit(TokPunct, i+1)
		}
	}
	return out
}

// cursorSplit returns the source without the "|" cursor marker and its offset.
func cursorSplit(s string) (string, int) {
	i := strings.Index(s, "|")
	if i < 0 {
		return s, len(s)
	}
	return s[:i] + s[i+1:], i
}

func TestCompletionAt(t *testing.T) {
	type tc struct {
		name   string
		src    string
		kind   CompletionKind
		prefix string
		qual   []string
		table  []string // Table.Parts for CompleteColumns
		found  bool
		quote  byte
	}
	cases := []tc{
		{name: "empty", src: "|", kind: CompleteAny},
		{name: "select list", src: "SELECT na|", kind: CompleteAny, prefix: "na"},
		{name: "from", src: "SELECT * FROM |", kind: CompleteTables},
		{name: "from prefix", src: "SELECT * FROM cus|", kind: CompleteTables, prefix: "cus"},
		{name: "from lowercase", src: "select * from cus|", kind: CompleteTables, prefix: "cus"},
		{name: "join", src: "SELECT * FROM a LEFT JOIN |", kind: CompleteTables},
		{name: "straight_join", src: "SELECT * FROM a STRAIGHT_JOIN o|", kind: CompleteTables, prefix: "o"},
		{name: "into", src: "INSERT INTO |", kind: CompleteTables},
		{name: "insert ignore into", src: "INSERT IGNORE INTO c|", kind: CompleteTables, prefix: "c"},
		{name: "update", src: "UPDATE |", kind: CompleteTables},
		{name: "table", src: "ALTER TABLE |", kind: CompleteTables},
		{name: "describe", src: "DESCRIBE |", kind: CompleteTables},
		{name: "desc statement", src: "DESC cu|", kind: CompleteTables, prefix: "cu"},
		{name: "order by desc is not a table", src: "SELECT a FROM t ORDER BY a DESC |", kind: CompleteAny},
		{name: "truncate", src: "TRUNCATE |", kind: CompleteTables},
		{name: "comma in from", src: "SELECT * FROM a x, |", kind: CompleteTables},
		{name: "comma after subquery in from", src: "SELECT * FROM (SELECT a, b FROM z) x, |", kind: CompleteTables},
		{name: "comma in select", src: "SELECT a, |", kind: CompleteAny},
		{name: "comma in IN list", src: "SELECT * FROM t WHERE a IN (1, |", kind: CompleteAny},
		{name: "comma in update", src: "UPDATE a, |", kind: CompleteTables},
		{name: "after from table", src: "SELECT * FROM t |", kind: CompleteAny},
		{name: "where", src: "SELECT * FROM t WHERE |", kind: CompleteAny},
		{name: "db dot in from", src: "SELECT * FROM shop.|", kind: CompleteObjects, qual: []string{"shop"}},
		{name: "db dot prefix in from", src: "SELECT * FROM shop.cu|", kind: CompleteObjects, prefix: "cu", qual: []string{"shop"}},
		{name: "db schema dot", src: "SELECT * FROM app.public.|", kind: CompleteObjects, qual: []string{"app", "public"}},
		{name: "db dot in join", src: "SELECT * FROM a JOIN hr.|", kind: CompleteObjects, qual: []string{"hr"}},
		{name: "call", src: "CALL |", kind: CompleteProcedures},
		{name: "call prefix", src: "CALL do_th|", kind: CompleteProcedures, prefix: "do_th"},
		{name: "call qualified", src: "CALL shop.|", kind: CompleteProcedures, qual: []string{"shop"}},
		{name: "exec", src: "EXEC dbo.us|", kind: CompleteProcedures, prefix: "us", qual: []string{"dbo"}},
		{name: "execute", src: "EXECUTE |", kind: CompleteProcedures},

		// Alias resolution.
		{name: "alias before from", src: "SELECT c.| FROM customers c JOIN orders o ON o.cid = c.id",
			kind: CompleteColumns, qual: []string{"c"}, table: []string{"customers"}, found: true},
		{name: "alias second table", src: "SELECT c.id, o.| FROM customers c JOIN orders o ON o.cid = c.id",
			kind: CompleteColumns, qual: []string{"o"}, table: []string{"orders"}, found: true},
		{name: "alias with prefix", src: "SELECT o.to| FROM customers c JOIN orders o ON o.cid = c.id",
			kind: CompleteColumns, prefix: "to", qual: []string{"o"}, table: []string{"orders"}, found: true},
		{name: "alias qualified table AS", src: "SELECT * FROM shop.customers AS c WHERE c.|",
			kind: CompleteColumns, qual: []string{"c"}, table: []string{"shop", "customers"}, found: true},
		{name: "alias in ON", src: "SELECT * FROM customers c JOIN orders o ON o.|",
			kind: CompleteColumns, qual: []string{"o"}, table: []string{"orders"}, found: true},
		{name: "alias case-insensitive", src: "SELECT C.| FROM customers c",
			kind: CompleteColumns, qual: []string{"C"}, table: []string{"customers"}, found: true},
		{name: "table name as qualifier", src: "SELECT customers.| FROM customers",
			kind: CompleteColumns, qual: []string{"customers"}, table: []string{"customers"}, found: true},
		{name: "table name of aliased table", src: "SELECT customers.| FROM customers c",
			kind: CompleteColumns, qual: []string{"customers"}, table: []string{"customers"}, found: true},
		{name: "db.table qualifier", src: "SELECT shop.customers.| FROM shop.customers",
			kind: CompleteColumns, qual: []string{"shop", "customers"}, table: []string{"shop", "customers"}, found: true},
		{name: "unknown qualifier", src: "SELECT x.| FROM customers c",
			kind: CompleteColumns, qual: []string{"x"}, table: []string{"x"}, found: false},
		{name: "comma join alias", src: "SELECT * FROM customers c, orders o WHERE o.|",
			kind: CompleteColumns, qual: []string{"o"}, table: []string{"orders"}, found: true},
		{name: "update alias", src: "UPDATE customers c SET c.|",
			kind: CompleteColumns, qual: []string{"c"}, table: []string{"customers"}, found: true},
		{name: "delete alias", src: "DELETE FROM orders o WHERE o.|",
			kind: CompleteColumns, qual: []string{"o"}, table: []string{"orders"}, found: true},
		{name: "subquery alias inside", src: "SELECT * FROM customers c WHERE c.id IN (SELECT o.| FROM orders o)",
			kind: CompleteColumns, qual: []string{"o"}, table: []string{"orders"}, found: true},
		{name: "outer alias from subquery", src: "SELECT * FROM customers c WHERE EXISTS (SELECT 1 FROM orders o WHERE o.cid = c.|)",
			kind: CompleteColumns, qual: []string{"c"}, table: []string{"customers"}, found: true},
		{name: "derived table alias", src: "SELECT d.| FROM (SELECT id FROM customers) AS d",
			kind: CompleteColumns, qual: []string{"d"}, table: nil, found: true},
		{name: "multiline", src: "SELECT\n  c.|\nFROM\n  customers\n    AS c\nWHERE 1",
			kind: CompleteColumns, qual: []string{"c"}, table: []string{"customers"}, found: true},
		{name: "spaces around dot", src: "SELECT c . | FROM customers c",
			kind: CompleteColumns, qual: []string{"c"}, table: []string{"customers"}, found: true},

		// Quoted identifiers (MySQL backticks via the MySQL tokenizer below).
		{name: "backtick table alias", src: "SELECT `c`.| FROM `shop`.`customers` `c`",
			kind: CompleteColumns, qual: []string{"c"}, table: []string{"shop", "customers"}, found: true},
		{name: "backtick prefix", src: "SELECT * FROM `cus|", kind: CompleteTables, prefix: "cus", quote: '`'},
		{name: "backtick prefix after dot", src: "SELECT c.`na| FROM customers c",
			kind: CompleteColumns, prefix: "na", qual: []string{"c"}, table: []string{"customers"}, found: true, quote: '`'},
		{name: "backtick doubled", src: "SELECT * FROM `a``b|", kind: CompleteTables, prefix: "a`b", quote: '`'},

		// No completion in strings, comments, numbers, variables.
		{name: "in string", src: "SELECT 'abc|", kind: CompleteNone},
		{name: "in closed string", src: "SELECT 'a|bc'", kind: CompleteNone},
		{name: "after closed string", src: "SELECT 'abc' |", kind: CompleteAny},
		{name: "in double-quoted mysql string", src: `SELECT "ab|`, kind: CompleteNone},
		{name: "in line comment", src: "SELECT 1 -- FROM |", kind: CompleteNone},
		{name: "in hash comment", src: "SELECT 1 # FROM |", kind: CompleteNone},
		{name: "in block comment", src: "SELECT /* FROM | */ 1", kind: CompleteNone},
		{name: "in unterminated block comment", src: "SELECT /* FROM |", kind: CompleteNone},
		{name: "after block comment", src: "SELECT * FROM /* x */ |", kind: CompleteTables},
		{name: "after line comment", src: "SELECT * FROM -- tables\n|", kind: CompleteTables},
		{name: "number", src: "SELECT 12|", kind: CompleteNone},
		{name: "variable", src: "SELECT @va|", kind: CompleteNone},
		{name: "dot after call", src: "SELECT f(x).|", kind: CompleteNone},

		// Statement bounds.
		{name: "second statement", src: "SELECT * FROM a x; SELECT x.|", kind: CompleteColumns,
			qual: []string{"x"}, table: []string{"x"}, found: false},
		{name: "first statement", src: "SELECT x.| FROM b; SELECT * FROM a x", kind: CompleteColumns,
			qual: []string{"x"}, table: []string{"x"}, found: false},
		{name: "after delimiter", src: "SELECT 1;|", kind: CompleteAny},
		{name: "custom delimiter", src: "DELIMITER $$\nSELECT * FROM a x$$\nSELECT * FROM |", kind: CompleteTables},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, cur := cursorSplit(c.src)
			got := CompletionAt(NewMySQL(), src, cur)
			if got.Kind != c.kind {
				t.Fatalf("kind = %v, want %v (%+v)", got.Kind, c.kind, got)
			}
			if c.kind == CompleteNone {
				return
			}
			if got.Prefix != c.prefix {
				t.Errorf("prefix = %q, want %q", got.Prefix, c.prefix)
			}
			if !reflect.DeepEqual(got.Qualifier, c.qual) {
				t.Errorf("qualifier = %q, want %q", got.Qualifier, c.qual)
			}
			if got.Quote != c.quote {
				t.Errorf("quote = %q, want %q", got.Quote, c.quote)
			}
			wantStart := cur - len(c.prefix)
			if c.quote != 0 {
				wantStart = strings.LastIndexByte(src[:cur], c.quote)
				if c.name == "backtick doubled" {
					wantStart = strings.Index(src, "`")
				}
			}
			if got.Start != wantStart {
				t.Errorf("start = %d, want %d", got.Start, wantStart)
			}
			if c.kind == CompleteColumns {
				if got.Table == nil {
					t.Fatal("no table")
				}
				if !reflect.DeepEqual(got.Table.Parts, c.table) || got.TableFound != c.found {
					t.Errorf("table = %q found=%v, want %q found=%v", got.Table.Parts, got.TableFound, c.table, c.found)
				}
			}
		})
	}
}

func TestCompletionTables(t *testing.T) {
	cases := []struct {
		src  string
		want []TableRef
	}{
		{"SELECT c.| FROM customers c JOIN orders o ON o.cid = c.id",
			[]TableRef{{Parts: []string{"customers"}, Alias: "c"}, {Parts: []string{"orders"}, Alias: "o"}}},
		{"SELECT | FROM shop.customers AS c, hr.staff s, plain WHERE 1",
			[]TableRef{{Parts: []string{"shop", "customers"}, Alias: "c"}, {Parts: []string{"hr", "staff"}, Alias: "s"}, {Parts: []string{"plain"}}}},
		{"SELECT | FROM a LEFT OUTER JOIN b USING (id) NATURAL JOIN c",
			[]TableRef{{Parts: []string{"a"}}, {Parts: []string{"b"}}, {Parts: []string{"c"}}}},
		{"INSERT INTO t (a, |) SELECT x FROM s",
			[]TableRef{{Parts: []string{"t"}}, {Parts: []string{"s"}}}},
		{"UPDATE t1 a, t2 b SET a.x = | WHERE a.id = b.id",
			[]TableRef{{Parts: []string{"t1"}, Alias: "a"}, {Parts: []string{"t2"}, Alias: "b"}}},
		{"SELECT | FROM (SELECT 1 FROM inner_t) d JOIN outer_t o ON 1",
			[]TableRef{{Alias: "d"}, {Parts: []string{"inner_t"}}, {Parts: []string{"outer_t"}, Alias: "o"}}},
		{"CREATE TABLE IF NOT EXISTS newt (id INT)|", []TableRef{{Parts: []string{"newt"}}}},
		{"SELECT | FROM t WHERE x = 1 ORDER BY a", []TableRef{{Parts: []string{"t"}}}},
		{"SELECT | FROM t1 FORCE INDEX (ix) JOIN t2 ON 1", []TableRef{{Parts: []string{"t1"}}, {Parts: []string{"t2"}}}},
		{"SELECT `a b`.| FROM `my db`.`a b`", []TableRef{{Parts: []string{"my db", "a b"}}}},
	}
	for _, c := range cases {
		src, cur := cursorSplit(c.src)
		got := CompletionAt(NewMySQL(), src, cur)
		if !reflect.DeepEqual(got.Tables, c.want) {
			t.Errorf("%s\n tables = %+v\n   want %+v", c.src, got.Tables, c.want)
		}
	}
}

func TestCompletionANSIQuoting(t *testing.T) {
	cases := []struct {
		src    string
		kind   CompletionKind
		prefix string
		quote  byte
		table  []string
		qual   []string
	}{
		{`SELECT "c".| FROM "public"."customers" "c"`, CompleteColumns, "", 0, []string{"public", "customers"}, []string{"c"}},
		{`SELECT [c].| FROM [dbo].[customers] AS [c]`, CompleteColumns, "", 0, []string{"dbo", "customers"}, []string{"c"}},
		{`SELECT * FROM "cus|`, CompleteTables, "cus", '"', nil, nil},
		{`SELECT * FROM [cus|`, CompleteTables, "cus", '[', nil, nil},
		{`SELECT * FROM [a]]b|`, CompleteTables, "a]b", '[', nil, nil},
		{`SELECT c."na| FROM customers c`, CompleteColumns, "na", '"', []string{"customers"}, []string{"c"}},
		{`SELECT [c].[na| FROM customers c`, CompleteColumns, "na", '[', []string{"customers"}, []string{"c"}},
		{`SELECT * FROM sales.|`, CompleteObjects, "", 0, nil, []string{"sales"}},
		{`SELECT * FROM "my db".|`, CompleteObjects, "", 0, nil, []string{"my db"}},
		{`SELECT 'x|`, CompleteNone, "", 0, nil, nil},
		{`SELECT * FROM t -- c|`, CompleteNone, "", 0, nil, nil},
		{"SELECT o.| FROM orders o; SELECT c.x FROM customers c", CompleteColumns, "", 0, []string{"orders"}, []string{"o"}},
	}
	for _, c := range cases {
		src, cur := cursorSplit(c.src)
		got := CompletionAt(ansiTok{}, src, cur)
		if got.Kind != c.kind {
			t.Errorf("%s: kind %v, want %v", c.src, got.Kind, c.kind)
			continue
		}
		if c.kind == CompleteNone {
			continue
		}
		if got.Prefix != c.prefix || got.Quote != c.quote || !reflect.DeepEqual(got.Qualifier, c.qual) {
			t.Errorf("%s: prefix %q quote %q qual %q", c.src, got.Prefix, got.Quote, got.Qualifier)
		}
		if c.table != nil && (got.Table == nil || !reflect.DeepEqual(got.Table.Parts, c.table) || !got.TableFound) {
			t.Errorf("%s: table %+v", c.src, got.Table)
		}
	}
}

func TestCompletionCursorBounds(t *testing.T) {
	if got := CompletionAt(NewMySQL(), "SELECT", -5); got.Kind != CompleteAny || got.Start != 0 {
		t.Errorf("negative cursor: %+v", got)
	}
	if got := CompletionAt(NewMySQL(), "SELECT * FROM ", 999); got.Kind != CompleteTables {
		t.Errorf("cursor past end: %+v", got)
	}
}

func TestUnterminated(t *testing.T) {
	cases := map[string]bool{
		`'abc'`: false, `'abc`: true, `'it''s'`: false, `'abc''`: true, `''`: false, `'''`: true,
		`'a\'`: true, `'a\\'`: false, `E'x'`: false, `N'x`: true, `$$abc$$`: false, `$$abc`: true,
		`$t$abc$t$`: false, `$t$abc$`: true, `"x"`: false, `"x`: true,
	}
	for s, want := range cases {
		if got := unterminated(Token{Kind: TokString, Text: s}); got != want {
			t.Errorf("string %s: %v, want %v", s, got, want)
		}
	}
	comments := map[string]bool{"/* x */": false, "/* x": true, "/*/": true, "-- x": true, "-- x\n": false}
	for s, want := range comments {
		if got := unterminated(Token{Kind: TokComment, Text: s}); got != want {
			t.Errorf("comment %q: %v, want %v", s, got, want)
		}
	}
}
