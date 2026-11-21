package sqltext

import (
	"reflect"
	"strings"
	"testing"
)

func mysqlTexts(t *testing.T, src string) []string {
	t.Helper()
	stmts, err := NewMySQL().Split(src)
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

func checkStmtInvariants(t *testing.T, src string, stmts []Statement) {
	t.Helper()
	prevEnd := 0
	for i, s := range stmts {
		if s.Count != 1 {
			t.Errorf("stmt %d: Count = %d", i, s.Count)
		}
		if s.Start < prevEnd || s.End < s.Start+len(s.Text) || s.End > len(src) {
			t.Errorf("stmt %d: bad range Start=%d End=%d prevEnd=%d", i, s.Start, s.End, prevEnd)
			continue
		}
		if got := src[s.Start : s.Start+len(s.Text)]; got != s.Text {
			t.Errorf("stmt %d: src[Start:] = %q, Text = %q", i, got, s.Text)
		}
		if s.Text != strings.TrimSpace(s.Text) || s.Text == "" {
			t.Errorf("stmt %d: Text not trimmed: %q", i, s.Text)
		}
		if want := 1 + strings.Count(src[:s.Start], "\n"); s.Line != want {
			t.Errorf("stmt %d: Line = %d, want %d", i, s.Line, want)
		}
		prevEnd = s.End
	}
}

func TestMySQLSplit(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", " \n\t\r\n ", nil},
		{"only comments", "-- a\n# b\n/* c */\n", nil},
		{"only delimiters", ";;\n ; ", nil},
		{"single no delimiter", "SELECT 1", []string{"SELECT 1"}},
		{"single with delimiter", "SELECT 1;", []string{"SELECT 1"}},
		{"two on one line", "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"last without delimiter", "SELECT 1;\nSELECT 2\n", []string{"SELECT 1", "SELECT 2"}},
		{"multi-line", "SELECT a,\n  b\nFROM t\n;\n", []string{"SELECT a,\n  b\nFROM t"}},
		{"semicolon in single quotes", "SELECT 'a;b'; SELECT 2", []string{"SELECT 'a;b'", "SELECT 2"}},
		{"semicolon in double quotes", `SELECT "a;b"; SELECT 2`, []string{`SELECT "a;b"`, "SELECT 2"}},
		{"backslash escaped quote", `SELECT 'it\'s;'; SELECT 2`, []string{`SELECT 'it\'s;'`, "SELECT 2"}},
		{"doubled quote", `SELECT 'it''s;'; SELECT 2`, []string{`SELECT 'it''s;'`, "SELECT 2"}},
		{"escaped backslash before quote", `SELECT 'a\\'; SELECT 2`, []string{`SELECT 'a\\'`, "SELECT 2"}},
		{"doubled double quote", `SELECT "a"";b"; SELECT 2`, []string{`SELECT "a"";b"`, "SELECT 2"}},
		{"backtick ident", "SELECT `a;b` FROM t; SELECT 2", []string{"SELECT `a;b` FROM t", "SELECT 2"}},
		{"doubled backtick", "SELECT `a``;b`; SELECT 2", []string{"SELECT `a``;b`", "SELECT 2"}},
		{"backslash in backtick is literal", "SELECT `a\\`; SELECT 2", []string{"SELECT `a\\`", "SELECT 2"}},
		{"dash comment", "SELECT 1 -- x;y\n; SELECT 2", []string{"SELECT 1 -- x;y", "SELECT 2"}},
		{"dash comment tab", "SELECT 1 --\tx;y\n;", []string{"SELECT 1 --\tx;y"}},
		{"dash comment at eof", "SELECT 1 --", []string{"SELECT 1 --"}},
		{"dash no space is not comment", "SELECT 1 --x; SELECT 2", []string{"SELECT 1 --x", "SELECT 2"}},
		{"hash comment", "SELECT 1 # a;b\n; SELECT 2", []string{"SELECT 1 # a;b", "SELECT 2"}},
		{"block comment", "SELECT /* ; */ 1; SELECT 2", []string{"SELECT /* ; */ 1", "SELECT 2"}},
		{"multiline block comment", "SELECT /* a\n;\nb */ 1;", []string{"SELECT /* a\n;\nb */ 1"}},
		{"exec comment", "/*!40101 SET NAMES utf8 */;\nSELECT 1;", []string{"/*!40101 SET NAMES utf8 */", "SELECT 1"}},
		{"exec comment with semicolon", "/*!50003 CREATE TRIGGER x; y */; SELECT 1", []string{"/*!50003 CREATE TRIGGER x; y */", "SELECT 1"}},
		{"mariadb exec comment", "/*M!100100 SET x=1 */;", []string{"/*M!100100 SET x=1 */"}},
		{"leading comment kept", "-- header\nSELECT 1;", []string{"-- header\nSELECT 1"}},
		{"trailing same-line comment dropped from next", "SELECT 1; -- one\nSELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"comment on own line kept with next", "SELECT 1;\n-- two\nSELECT 2;", []string{"SELECT 1", "-- two\nSELECT 2"}},
		{"comment-only between statements dropped", "SELECT 1;\n/* x */;\nSELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"unterminated string", "SELECT 1; SELECT 'abc;\n", []string{"SELECT 1", "SELECT 'abc;"}},
		{"unterminated comment", "SELECT 1; SELECT /* x;", []string{"SELECT 1", "SELECT /* x;"}},
		{"unterminated backtick", "SELECT `x;", []string{"SELECT `x;"}},
		{"hex and bit strings", "SELECT X';', b'1', N';'; SELECT 2", []string{"SELECT X';', b'1', N';'", "SELECT 2"}},
		{"crlf", "SELECT 1;\r\nSELECT 2;\r\n", []string{"SELECT 1", "SELECT 2"}},
		{"user variables", "SET @a := 1; SELECT @@version;", []string{"SET @a := 1", "SELECT @@version"}},
		{"utf8", "SELECT 'ä;ö', größe FROM t;", []string{"SELECT 'ä;ö', größe FROM t"}},

		// DELIMITER
		{"delimiter $$ procedure", "DELIMITER $$\nCREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND$$\nDELIMITER ;\nCALL p();\n",
			[]string{"CREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND", "CALL p()"}},
		{"delimiter // trigger", "delimiter //\nCREATE TRIGGER t BEFORE INSERT ON x FOR EACH ROW\nBEGIN\n  SET NEW.a = 1;\nEND //\ndelimiter ;\nSELECT 1;",
			[]string{"CREATE TRIGGER t BEFORE INSERT ON x FOR EACH ROW\nBEGIN\n  SET NEW.a = 1;\nEND", "SELECT 1"}},
		{"delimiter ;;", "DELIMITER ;;\nSELECT 1; SELECT 2;;\nDELIMITER ;\nSELECT 3;", []string{"SELECT 1; SELECT 2", "SELECT 3"}},
		{"delimiter mixed case and indent", "  DeLiMiTeR $$\nSELECT 1$$\n\tdelimiter ;\nSELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"multiple switches", "SELECT 0;\nDELIMITER $$\nSELECT 1; $$\nDELIMITER //\nSELECT 2$$ //\nDELIMITER ;\nSELECT 3;",
			[]string{"SELECT 0", "SELECT 1;", "SELECT 2$$", "SELECT 3"}},
		{"custom delimiter in strings and comments", "DELIMITER $$\nSELECT '$$', \"$$\", `$$` /* $$ */ -- $$\n, 1 # $$\n$$\nDELIMITER ;",
			[]string{"SELECT '$$', \"$$\", `$$` /* $$ */ -- $$\n, 1 # $$"}},
		{"custom delimiter inside identifier", "DELIMITER $$\nSELECT a$$SELECT b$$", []string{"SELECT a", "SELECT b"}},
		{"slash delimiter vs comment and division", "DELIMITER //\nSELECT 4 / 2, /* c */ 1//\nDELIMITER ;", []string{"SELECT 4 / 2, /* c */ 1"}},
		{"delimiter without trailing delimiter", "DELIMITER $$\nSELECT 1", []string{"SELECT 1"}},
		{"delimiter crlf", "DELIMITER $$\r\nSELECT 1;\r\nEND$$\r\nDELIMITER ;\r\nSELECT 2;\r\n", []string{"SELECT 1;\r\nEND", "SELECT 2"}},
		{"delimiter argument first word", "DELIMITER $$ ignored\nSELECT 1$$", []string{"SELECT 1"}},
		{"delimiter quoted argument", "DELIMITER '$ $'\nSELECT 1$ $SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"delimiter without argument keeps", "DELIMITER\nSELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"delimiter with backslash rejected", "DELIMITER \\x\nSELECT 1; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		{"delimiter after comments", "-- setup\n/* x */\nDELIMITER $$\nSELECT 1;$$", []string{"SELECT 1;"}},
		{"delimiter inside unfinished statement is text", "SELECT 1\nDELIMITER $$\n;SELECT 2;", []string{"SELECT 1\nDELIMITER $$", "SELECT 2"}},
		{"delimiter not at line start", "SELECT 1; DELIMITER $$\nSELECT 2$$;", []string{"SELECT 1", "DELIMITER $$\nSELECT 2$$"}},
		{"delimiter after block comment on same line", "/* c */ DELIMITER $$\n;", []string{"/* c */ DELIMITER $$"}},
		{"delimiter prefix word is not directive", "delimiters;", []string{"delimiters"}},
		{"delimiter as column", "SELECT\ndelimiter FROM t;", []string{"SELECT\ndelimiter FROM t"}},
		{"delimiter after newline following statement", "SELECT 1;\nDELIMITER $$\nSELECT 2$$", []string{"SELECT 1", "SELECT 2"}},
		{"exec comment with custom delimiter", "DELIMITER ;;\n/*!50003 CREATE PROCEDURE p() BEGIN SELECT 1;; END */;;\nDELIMITER ;",
			[]string{"/*!50003 CREATE PROCEDURE p() BEGIN SELECT 1;; END */"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mysqlTexts(t, tt.src)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Split(%q)\n got  %q\n want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestMySQLSplitPositions(t *testing.T) {
	src := "-- header\r\nSELECT 1;\r\n\r\nDELIMITER $$\r\nCREATE PROCEDURE p()\r\nBEGIN\r\n  SELECT 2;\r\nEND$$\r\nDELIMITER ;\r\n  SELECT 3"
	stmts, err := NewMySQL().Split(src)
	if err != nil {
		t.Fatal(err)
	}
	checkStmtInvariants(t, src, stmts)
	type pos struct {
		line       int
		start, end int
	}
	p1 := strings.Index(src, "-- header")
	p2 := strings.Index(src, "CREATE")
	p3 := strings.Index(src, "SELECT 3")
	want := []pos{
		{1, p1, strings.Index(src, "1;") + 2},
		{5, p2, strings.Index(src, "END$$") + 5},
		{10, p3, len(src)},
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
	if stmts[0].Text != "-- header\r\nSELECT 1" {
		t.Errorf("stmt 0 text %q", stmts[0].Text)
	}
}

func TestMySQLStatementAt(t *testing.T) {
	src := "\n  SELECT 1;  \n\nSELECT 2; -- c\nDELIMITER $$\nBEGIN SELECT 3; END$$\n\n"
	m := NewMySQL()
	at := func(sub string, off int) int {
		i := strings.Index(src, sub)
		if i < 0 {
			t.Fatalf("%q not in src", sub)
		}
		return i + off
	}
	tests := []struct {
		name string
		pos  int
		want string
	}{
		{"before first", 0, "SELECT 1"},
		{"start of first", at("SELECT 1", 0), "SELECT 1"},
		{"inside first", at("SELECT 1", 3), "SELECT 1"},
		{"on first delimiter", at("1;", 1), "SELECT 1"},
		{"just after first delimiter", at("1;", 2), "SELECT 1"},
		{"blank lines after first", at("\n\nSELECT 2", 1), "SELECT 1"},
		{"start of second", at("SELECT 2", 0), "SELECT 2"},
		{"trailing comment of second", at("-- c", 2), "SELECT 2"},
		{"on DELIMITER line", at("DELIMITER", 3), "SELECT 2"},
		{"inner semicolon of block", at("3;", 1), "BEGIN SELECT 3; END"},
		{"on $$", at("END$$", 4), "BEGIN SELECT 3; END"},
		{"end of input", len(src), "BEGIN SELECT 3; END"},
		{"beyond input", len(src) + 10, "BEGIN SELECT 3; END"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, ok := m.StatementAt(src, tt.pos)
			if !ok || s.Text != tt.want {
				t.Errorf("StatementAt(%d) = %q, %v; want %q", tt.pos, s.Text, ok, tt.want)
			}
		})
	}
	for _, empty := range []string{"", "  \n", "-- only\n/* c */", "DELIMITER $$\n"} {
		if s, ok := m.StatementAt(empty, 0); ok {
			t.Errorf("StatementAt(%q) = %+v, want !ok", empty, s)
		}
	}
	// Respects DELIMITER state: with "$$" active, ";" does not split.
	src2 := "DELIMITER $$\nSELECT 1; SELECT 2$$"
	s, ok := m.StatementAt(src2, strings.Index(src2, "SELECT 2"))
	if !ok || s.Text != "SELECT 1; SELECT 2" {
		t.Errorf("StatementAt with $$ = %q, %v", s.Text, ok)
	}
}

type tk struct {
	k TokenKind
	s string
}

func TestMySQLTokenize(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []tk
	}{
		{"empty", "", nil},
		{"simple", "SELECT a, 1.5e3 FROM `t``x` WHERE b = 'q\\'s' AND c <> ?;",
			[]tk{{TokIdent, "SELECT"}, {TokWhitespace, " "}, {TokIdent, "a"}, {TokPunct, ","}, {TokWhitespace, " "},
				{TokNumber, "1.5e3"}, {TokWhitespace, " "}, {TokIdent, "FROM"}, {TokWhitespace, " "}, {TokQuotedIdent, "`t``x`"},
				{TokWhitespace, " "}, {TokIdent, "WHERE"}, {TokWhitespace, " "}, {TokIdent, "b"}, {TokWhitespace, " "},
				{TokPunct, "="}, {TokWhitespace, " "}, {TokString, `'q\'s'`}, {TokWhitespace, " "}, {TokIdent, "AND"},
				{TokWhitespace, " "}, {TokIdent, "c"}, {TokWhitespace, " "}, {TokPunct, "<>"}, {TokWhitespace, " "},
				{TokParam, "?"}, {TokDelimiter, ";"}}},
		{"comments", "-- a\r\n#b\n--x /* c */ /*!40101 d */",
			[]tk{{TokComment, "-- a"}, {TokWhitespace, "\r\n"}, {TokComment, "#b"}, {TokWhitespace, "\n"},
				{TokPunct, "-"}, {TokPunct, "-"}, {TokIdent, "x"}, {TokWhitespace, " "}, {TokComment, "/* c */"},
				{TokWhitespace, " "}, {TokExecComment, "/*!40101 d */"}}},
		{"variables", "@a:=@@session.x",
			[]tk{{TokIdent, "@a"}, {TokPunct, ":="}, {TokIdent, "@@session"}, {TokPunct, "."}, {TokIdent, "x"}}},
		{"lone at", "@'u'", []tk{{TokPunct, "@"}, {TokString, "'u'"}}},
		{"numbers", "0x1F 0b10 .5 12 1abc t.c",
			[]tk{{TokNumber, "0x1F"}, {TokWhitespace, " "}, {TokNumber, "0b10"}, {TokWhitespace, " "}, {TokNumber, ".5"},
				{TokWhitespace, " "}, {TokNumber, "12"}, {TokWhitespace, " "}, {TokIdent, "1abc"}, {TokWhitespace, " "},
				{TokIdent, "t"}, {TokPunct, "."}, {TokIdent, "c"}}},
		{"hex string", "x'0A'", []tk{{TokString, "x'0A'"}}},
		{"directive", "DELIMITER $$ junk\nEND$$",
			[]tk{{TokIdent, "DELIMITER"}, {TokWhitespace, " "}, {TokDelimiter, "$$"}, {TokWhitespace, " "}, {TokComment, "junk"},
				{TokWhitespace, "\n"}, {TokIdent, "END"}, {TokDelimiter, "$$"}}},
		{"slash delimiter", "DELIMITER //\n4/2//",
			[]tk{{TokIdent, "DELIMITER"}, {TokWhitespace, " "}, {TokDelimiter, "//"}, {TokWhitespace, "\n"},
				{TokNumber, "4"}, {TokPunct, "/"}, {TokNumber, "2"}, {TokDelimiter, "//"}}},
		{"semicolon is punct under custom delimiter", "DELIMITER $$\n;",
			[]tk{{TokIdent, "DELIMITER"}, {TokWhitespace, " "}, {TokDelimiter, "$$"}, {TokWhitespace, "\n"}, {TokPunct, ";"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := NewMySQL().Tokenize(tt.src)
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

func checkTokenInvariants(t *testing.T, src string, toks []Token) {
	t.Helper()
	pos, line := 0, 1
	for i, x := range toks {
		if x.Start != pos || x.End <= x.Start || src[x.Start:x.End] != x.Text {
			t.Fatalf("token %d %+v not contiguous at %d", i, x, pos)
		}
		if x.Line != line {
			t.Errorf("token %d %q: Line = %d, want %d", i, x.Text, x.Line, line)
		}
		line += strings.Count(x.Text, "\n")
		pos = x.End
	}
	if pos != len(src) {
		t.Errorf("tokens end at %d, src len %d", pos, len(src))
	}
}

func TestMySQLTokenizeLinesCRLF(t *testing.T) {
	src := "SELECT 1;\r\n/* a\r\nb */\r\nSELECT\r\n'x\r\ny' ;"
	toks := NewMySQL().Tokenize(src)
	checkTokenInvariants(t, src, toks)
	lines := map[string]int{}
	for _, x := range toks {
		if x.Kind != TokWhitespace {
			lines[x.Text] = x.Line
		}
	}
	want := map[string]int{"SELECT": 4, "/* a\r\nb */": 2, "'x\r\ny'": 5, "1": 1}
	for k, v := range want {
		if lines[k] != v {
			t.Errorf("line of %q = %d, want %d", k, lines[k], v)
		}
	}
}

// Fuzz-ish robustness: arbitrary prefixes of tricky input never panic and
// keep the invariants.
func TestMySQLPrefixes(t *testing.T) {
	src := "DELIMITER $$\r\nSELECT 'a\\'b', \"c\"\"d\", `e``f` /*! x */ -- y\n# z\n/* w */ 0x1F, @@v$$\ndelimiter '//'\nSELECT 1//\nDELIMITER ;\nSELECT X'0'; --"
	m := NewMySQL()
	for i := 0; i <= len(src); i++ {
		p := src[:i]
		checkTokenInvariants(t, p, m.Tokenize(p))
		stmts, _ := m.Split(p)
		checkStmtInvariants(t, p, stmts)
		for pos := 0; pos <= len(p); pos += 7 {
			m.StatementAt(p, pos)
		}
	}
}
