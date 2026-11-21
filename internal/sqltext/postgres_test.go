package sqltext

import (
	"reflect"
	"strings"
	"testing"
)

// checkStmtInvariants, checkTokenInvariants and tk are shared with
// mysql_test.go.

func pgTexts(t *testing.T, src string) []string {
	t.Helper()
	stmts, err := NewPostgres().Split(src)
	if err != nil {
		t.Fatalf("Split error: %v", err)
	}
	checkStmtInvariants(t, src, stmts)
	var out []string
	for _, s := range stmts {
		out = append(out, s.Text)
	}
	return out
}

func TestPostgresSplit(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", " \n\t\r\n ", nil},
		{"only comments", "-- a\n/* b /* nested */ */\n", nil},
		{"only delimiters", ";;\n ; ", nil},
		{"single no delimiter", "SELECT 1", []string{"SELECT 1"}},
		{"two on one line", "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"multi-line", "SELECT a,\n  b\nFROM t\n;\n", []string{"SELECT a,\n  b\nFROM t"}},
		{"semicolon in string", "SELECT 'a;b'; SELECT 2", []string{"SELECT 'a;b'", "SELECT 2"}},
		{"doubled quote", "SELECT 'it''s;'; SELECT 2", []string{"SELECT 'it''s;'", "SELECT 2"}},
		{"backslash is literal in standard string", `SELECT 'a\'; SELECT 2`, []string{`SELECT 'a\'`, "SELECT 2"}},
		{"E string backslash escape", `SELECT E'it\'s;'; SELECT 2`, []string{`SELECT E'it\'s;'`, "SELECT 2"}},
		{"e string lower case", `SELECT e'\\'; SELECT 2`, []string{`SELECT e'\\'`, "SELECT 2"}},
		{"E string doubled quote", `SELECT E'a'';b'; SELECT 2`, []string{`SELECT E'a'';b'`, "SELECT 2"}},
		{"bit hex national unicode strings", `SELECT B'1', X'1;', N'x;', U&'d\0061t;'; SELECT 2`,
			[]string{`SELECT B'1', X'1;', N'x;', U&'d\0061t;'`, "SELECT 2"}},
		{"quoted ident", `SELECT "a;b" FROM "t""x;"; SELECT 2`, []string{`SELECT "a;b" FROM "t""x;"`, "SELECT 2"}},
		{"unicode quoted ident", `SELECT U&"a;b"; SELECT 2`, []string{`SELECT U&"a;b"`, "SELECT 2"}},
		{"dollar quote", "SELECT $$a;b$$; SELECT 2", []string{"SELECT $$a;b$$", "SELECT 2"}},
		{"tagged dollar quote", "SELECT $x$ $$; $x$; SELECT 2", []string{"SELECT $x$ $$; $x$", "SELECT 2"}},
		{"dollar tag with digits and underscore", "SELECT $_t1$;'$_t1$;", []string{"SELECT $_t1$;'$_t1$"}},
		{"dollar inside identifier is not a quote", "SELECT a$b$c; SELECT 2", []string{"SELECT a$b$c", "SELECT 2"}},
		{"positional params", "SELECT $1, $2::int; SELECT 2", []string{"SELECT $1, $2::int", "SELECT 2"}},
		{"function body", "CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $body$\nBEGIN\n  RETURN 1;\nEND;\n$body$;\nSELECT f();",
			[]string{"CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $body$\nBEGIN\n  RETURN 1;\nEND;\n$body$", "SELECT f()"}},
		{"dash comment", "SELECT 1 -- x;y\n; SELECT 2", []string{"SELECT 1 -- x;y", "SELECT 2"}},
		{"dash comment without space", "SELECT 1 --x;y\n; SELECT 2", []string{"SELECT 1 --x;y", "SELECT 2"}},
		{"block comment", "SELECT /* ; */ 1; SELECT 2", []string{"SELECT /* ; */ 1", "SELECT 2"}},
		{"nested block comment", "SELECT /* a /* b; */ c; */ 1; SELECT 2", []string{"SELECT /* a /* b; */ c; */ 1", "SELECT 2"}},
		{"leading comment kept", "-- header\nSELECT 1;", []string{"-- header\nSELECT 1"}},
		{"trailing same-line comment dropped from next", "SELECT 1; -- one\nSELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"comment on own line kept with next", "SELECT 1;\n-- two\nSELECT 2;", []string{"SELECT 1", "-- two\nSELECT 2"}},
		{"unterminated string", "SELECT 1; SELECT 'abc;\n", []string{"SELECT 1", "SELECT 'abc;"}},
		{"unterminated dollar", "SELECT 1; SELECT $$abc;", []string{"SELECT 1", "SELECT $$abc;"}},
		{"unterminated nested comment", "SELECT 1; SELECT /* /* */ x;", []string{"SELECT 1", "SELECT /* /* */ x;"}},
		{"crlf", "SELECT 1;\r\nSELECT 2;\r\n", []string{"SELECT 1", "SELECT 2"}},
		{"utf8", "SELECT 'ä;ö', größe FROM t;", []string{"SELECT 'ä;ö', größe FROM t"}},
		{"semicolon in parentheses (rule)", "CREATE RULE r AS ON INSERT TO t DO ALSO (INSERT INTO a VALUES (1); INSERT INTO b VALUES (2));\nSELECT 1;",
			[]string{"CREATE RULE r AS ON INSERT TO t DO ALSO (INSERT INTO a VALUES (1); INSERT INTO b VALUES (2))", "SELECT 1"}},
		{"begin atomic", "CREATE FUNCTION f(a int) RETURNS int LANGUAGE sql\nBEGIN ATOMIC\n  SELECT CASE WHEN a > 0 THEN 1 END;\n  SELECT a;\nEND;\nSELECT 2;",
			[]string{"CREATE FUNCTION f(a int) RETURNS int LANGUAGE sql\nBEGIN ATOMIC\n  SELECT CASE WHEN a > 0 THEN 1 END;\n  SELECT a;\nEND", "SELECT 2"}},
		{"or replace procedure begin atomic", "create or replace procedure p() begin atomic insert into t values (1); end; select 1",
			[]string{"create or replace procedure p() begin atomic insert into t values (1); end", "select 1"}},
		{"transaction begin splits", "BEGIN; SELECT 1; END; COMMIT;", []string{"BEGIN", "SELECT 1", "END", "COMMIT"}},
		{"case outside routine", "SELECT CASE WHEN true THEN 1 END; SELECT 2", []string{"SELECT CASE WHEN true THEN 1 END", "SELECT 2"}},
		{"do block", "DO $$ BEGIN RAISE NOTICE 'x;'; END $$;\nSELECT 1;", []string{"DO $$ BEGIN RAISE NOTICE 'x;'; END $$", "SELECT 1"}},
		{"unbalanced close paren does not block", "SELECT 1); SELECT 2", []string{"SELECT 1)", "SELECT 2"}},
		{"jsonb operators", "SELECT d ? 'k', d ?| array['a'], d @> '{}'; SELECT 2", []string{"SELECT d ? 'k', d ?| array['a'], d @> '{}'", "SELECT 2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pgTexts(t, tt.src)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Split(%q)\n got  %q\n want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestPostgresSplitPositions(t *testing.T) {
	src := "-- header\r\nSELECT 1;\r\n\r\nCREATE FUNCTION f() RETURNS int AS $$\r\nSELECT 2;\r\n$$ LANGUAGE sql;\r\n  SELECT 3"
	stmts, err := NewPostgres().Split(src)
	if err != nil {
		t.Fatal(err)
	}
	checkStmtInvariants(t, src, stmts)
	type pos struct{ line, start, end int }
	want := []pos{
		{1, 0, strings.Index(src, "1;") + 2},
		{4, strings.Index(src, "CREATE"), strings.Index(src, "sql;") + 4},
		{7, strings.Index(src, "SELECT 3"), len(src)},
	}
	if len(stmts) != len(want) {
		t.Fatalf("got %d statements: %+v", len(stmts), stmts)
	}
	for i, w := range want {
		s := stmts[i]
		if s.Line != w.line || s.Start != w.start || s.End != w.end {
			t.Errorf("stmt %d: got line=%d start=%d end=%d, want %+v (text %q)", i, s.Line, s.Start, s.End, w, s.Text)
		}
	}
}

func TestPostgresStatementAt(t *testing.T) {
	src := "\n  SELECT 1;  \n\nSELECT 2; -- c\nDO $$ BEGIN PERFORM 3; END $$;\n\n"
	p := NewPostgres()
	at := func(sub string, off int) int {
		i := strings.Index(src, sub)
		if i < 0 {
			t.Fatalf("%q not in src", sub)
		}
		return i + off
	}
	do := "DO $$ BEGIN PERFORM 3; END $$"
	tests := []struct {
		name string
		pos  int
		want string
	}{
		{"before first", 0, "SELECT 1"},
		{"inside first", at("SELECT 1", 3), "SELECT 1"},
		{"on first delimiter", at("1;", 1), "SELECT 1"},
		{"blank lines after first", at("\n\nSELECT 2", 1), "SELECT 1"},
		{"start of second", at("SELECT 2", 0), "SELECT 2"},
		{"trailing comment of second", at("-- c", 2), "SELECT 2"},
		{"inner semicolon of dollar body", at("3;", 1), do},
		{"end of input", len(src), do},
		{"beyond input", len(src) + 10, do},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ok := p.StatementAt(src, tt.pos)
			if !ok || s.Text != tt.want {
				t.Errorf("StatementAt(%d) = %q, %v; want %q", tt.pos, s.Text, ok, tt.want)
			}
		})
	}
	for _, empty := range []string{"", "  \n", "-- only\n/* c /* d */ */"} {
		if s, ok := p.StatementAt(empty, 0); ok {
			t.Errorf("StatementAt(%q) = %+v, want !ok", empty, s)
		}
	}
}

func TestPostgresTokenize(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []tk
	}{
		{"empty", "", nil},
		{"simple", `SELECT a::int, 1.5e3 FROM "t""x" WHERE b = E'q\'s' AND c <> $1;`,
			[]tk{{TokIdent, "SELECT"}, {TokWhitespace, " "}, {TokIdent, "a"}, {TokPunct, "::"}, {TokIdent, "int"}, {TokPunct, ","},
				{TokWhitespace, " "}, {TokNumber, "1.5e3"}, {TokWhitespace, " "}, {TokIdent, "FROM"}, {TokWhitespace, " "},
				{TokQuotedIdent, `"t""x"`}, {TokWhitespace, " "}, {TokIdent, "WHERE"}, {TokWhitespace, " "}, {TokIdent, "b"},
				{TokWhitespace, " "}, {TokPunct, "="}, {TokWhitespace, " "}, {TokString, `E'q\'s'`}, {TokWhitespace, " "},
				{TokIdent, "AND"}, {TokWhitespace, " "}, {TokIdent, "c"}, {TokWhitespace, " "}, {TokPunct, "<>"},
				{TokWhitespace, " "}, {TokParam, "$1"}, {TokDelimiter, ";"}}},
		{"comments", "-- a\r\n/* x /* y */ z */--b",
			[]tk{{TokComment, "-- a"}, {TokWhitespace, "\r\n"}, {TokComment, "/* x /* y */ z */"}, {TokComment, "--b"}}},
		{"operator cut before comment", "a+-- c\n+/*d*/b",
			[]tk{{TokIdent, "a"}, {TokPunct, "+"}, {TokComment, "-- c"}, {TokWhitespace, "\n"}, {TokPunct, "+"},
				{TokComment, "/*d*/"}, {TokIdent, "b"}}},
		{"dollar strings", "$$a$$ $t$b$t$ $1 $",
			[]tk{{TokString, "$$a$$"}, {TokWhitespace, " "}, {TokString, "$t$b$t$"}, {TokWhitespace, " "}, {TokParam, "$1"},
				{TokWhitespace, " "}, {TokPunct, "$"}}},
		{"question marks", "a ? b ?| c ?& d",
			[]tk{{TokIdent, "a"}, {TokWhitespace, " "}, {TokParam, "?"}, {TokWhitespace, " "}, {TokIdent, "b"}, {TokWhitespace, " "},
				{TokPunct, "?|"}, {TokWhitespace, " "}, {TokIdent, "c"}, {TokWhitespace, " "}, {TokPunct, "?&"}, {TokWhitespace, " "},
				{TokIdent, "d"}}},
		{"numbers", "0x1F 0o17 0b10 1_000 .5 12 t.c",
			[]tk{{TokNumber, "0x1F"}, {TokWhitespace, " "}, {TokNumber, "0o17"}, {TokWhitespace, " "}, {TokNumber, "0b10"},
				{TokWhitespace, " "}, {TokNumber, "1_000"}, {TokWhitespace, " "}, {TokNumber, ".5"}, {TokWhitespace, " "},
				{TokNumber, "12"}, {TokWhitespace, " "}, {TokIdent, "t"}, {TokPunct, "."}, {TokIdent, "c"}}},
		{"identifier with dollar", "a$1 x",
			[]tk{{TokIdent, "a$1"}, {TokWhitespace, " "}, {TokIdent, "x"}}},
		{"prefixed strings", "B'1' x'f' n'n' U&'u' u&\"i\" e'e'",
			[]tk{{TokString, "B'1'"}, {TokWhitespace, " "}, {TokString, "x'f'"}, {TokWhitespace, " "}, {TokString, "n'n'"},
				{TokWhitespace, " "}, {TokString, "U&'u'"}, {TokWhitespace, " "}, {TokQuotedIdent, `u&"i"`}, {TokWhitespace, " "},
				{TokString, "e'e'"}}},
		{"semicolon in parens is punct", "(;);",
			[]tk{{TokPunct, "("}, {TokPunct, ";"}, {TokPunct, ")"}, {TokDelimiter, ";"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := NewPostgres().Tokenize(tt.src)
			checkTokenInvariants(t, tt.src, toks)
			var got []tk
			for _, x := range toks {
				got = append(got, tk{x.Kind, x.Text})
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Tokenize(%q)\n got  %v\n want %v", tt.src, got, tt.want)
			}
		})
	}
}

func TestPostgresTokenizeLinesCRLF(t *testing.T) {
	src := "SELECT 1;\r\n/* a\r\nb */\r\nSELECT\r\n$$x\r\ny$$ ;"
	toks := NewPostgres().Tokenize(src)
	checkTokenInvariants(t, src, toks)
	lines := map[string]int{}
	for _, x := range toks {
		if x.Kind != TokWhitespace {
			lines[x.Text] = x.Line
		}
	}
	want := map[string]int{"SELECT": 4, "/* a\r\nb */": 2, "$$x\r\ny$$": 5, "1": 1}
	for k, v := range want {
		if lines[k] != v {
			t.Errorf("line of %q = %d, want %d", k, lines[k], v)
		}
	}
}

func TestPostgresKeywords(t *testing.T) {
	got := Keywords(NewPostgres(), "/* c */ WITH x AS (SELECT $$a$$) INSERT INTO t SELECT * FROM x RETURNING id", 0)
	want := []string{"WITH", "X", "AS", "SELECT", "INSERT", "INTO", "T", "SELECT", "FROM", "X", "RETURNING", "ID"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Keywords = %q", got)
	}
}

func TestPostgresPrefixes(t *testing.T) {
	src := "SELECT 'a''b', E'c\\'d', \"e\"\"f\" /* x /* y */ */ -- z\n$q$ ; $q$, $$;$$, $1 ?\r\nCREATE FUNCTION f() BEGIN ATOMIC SELECT (1;2); END; SELECT U&'x'; --"
	p := NewPostgres()
	for i := 0; i <= len(src); i++ {
		s := src[:i]
		checkTokenInvariants(t, s, p.Tokenize(s))
		stmts, _ := p.Split(s)
		checkStmtInvariants(t, s, stmts)
		for pos := 0; pos <= len(s); pos += 7 {
			p.StatementAt(s, pos)
		}
	}
}
