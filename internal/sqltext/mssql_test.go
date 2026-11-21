package sqltext

import (
	"reflect"
	"strings"
	"testing"
)

type msBatch struct {
	Text  string
	Count int
}

func mssqlBatches(t *testing.T, src string) []msBatch {
	t.Helper()
	stmts, err := NewMSSQL().Split(src)
	if err != nil {
		t.Fatalf("Split error: %v", err)
	}
	prevEnd := 0
	var out []msBatch
	for i, s := range stmts {
		if s.Start < prevEnd || s.End < s.Start+len(s.Text) || s.End > len(src) {
			t.Errorf("batch %d: bad range Start=%d End=%d prevEnd=%d", i, s.Start, s.End, prevEnd)
		} else if got := src[s.Start : s.Start+len(s.Text)]; got != s.Text {
			t.Errorf("batch %d: src[Start:] = %q, Text = %q", i, got, s.Text)
		}
		if s.Text != strings.TrimSpace(s.Text) || s.Text == "" {
			t.Errorf("batch %d: Text not trimmed: %q", i, s.Text)
		}
		if want := 1 + strings.Count(src[:s.Start], "\n"); s.Line != want {
			t.Errorf("batch %d: Line = %d, want %d", i, s.Line, want)
		}
		prevEnd = s.End
		out = append(out, msBatch{s.Text, s.Count})
	}
	return out
}

func TestMSSQLSplit(t *testing.T) {
	b := func(text string) msBatch { return msBatch{text, 1} }
	tests := []struct {
		name string
		src  string
		want []msBatch
	}{
		{"empty", "", nil},
		{"only GO", "GO\ngo\n  Go  \n", nil},
		{"only comments", "-- a\n/* b */\nGO\n-- c\n", nil},
		{"single no GO", "SELECT 1", []msBatch{b("SELECT 1")}},
		{"semicolons do not split", "SELECT 1; SELECT 2;\nSELECT 3", []msBatch{b("SELECT 1; SELECT 2;\nSELECT 3")}},
		{"two batches", "SELECT 1\nGO\nSELECT 2\nGO\n", []msBatch{b("SELECT 1"), b("SELECT 2")}},
		{"case and blanks", "SELECT 1\n  gO  \nSELECT 2", []msBatch{b("SELECT 1"), b("SELECT 2")}},
		{"count", "INSERT t DEFAULT VALUES\nGO 3\nSELECT 1\nGO", []msBatch{{"INSERT t DEFAULT VALUES", 3}, b("SELECT 1")}},
		{"count with tabs and comment", "SELECT 1\n\tGO\t12 -- twelve\nSELECT 2", []msBatch{{"SELECT 1", 12}, b("SELECT 2")}},
		{"GO with comment", "SELECT 1\nGO -- done\n-- next\nSELECT 2", []msBatch{b("SELECT 1"), b("-- next\nSELECT 2")}},
		{"GO comment no blank", "SELECT 1\nGO--x\nSELECT 2", []msBatch{b("SELECT 1"), b("SELECT 2")}},
		{"CRLF", "SELECT 1\r\nGO\r\nSELECT 2\r\n", []msBatch{b("SELECT 1"), b("SELECT 2")}},
		{"GO in string", "SELECT 'a\nGO\nb'\nGO", []msBatch{b("SELECT 'a\nGO\nb'")}},
		{"GO in N string", "SELECT N'it''s\nGO\n'", []msBatch{b("SELECT N'it''s\nGO\n'")}},
		{"GO in block comment", "SELECT 1 /*\nGO\n*/\nGO", []msBatch{b("SELECT 1 /*\nGO\n*/")}},
		{"GO in nested comment", "SELECT 1 /* /* */\nGO\n*/\nGO\nSELECT 2", []msBatch{b("SELECT 1 /* /* */\nGO\n*/"), b("SELECT 2")}},
		{"GO in bracket ident", "SELECT 1 AS [x\nGO\n]]y]\nGO", []msBatch{b("SELECT 1 AS [x\nGO\n]]y]")}},
		{"GO in quoted ident", "SELECT 1 AS \"x\nGO\n\"\nGO", []msBatch{b("SELECT 1 AS \"x\nGO\n\"")}},
		{"GO not alone", "SELECT 1 GO\nGO x\nGOTO l\nGO;\nGO3\n", []msBatch{b("SELECT 1 GO\nGO x\nGOTO l\nGO;\nGO3")}},
		{"GO after code on line", "SELECT 1 -- c\nGO", []msBatch{b("SELECT 1 -- c")}},
		{"line comment hides GO", "-- GO\nSELECT 1", []msBatch{b("-- GO\nSELECT 1")}},
		{"unterminated string", "SELECT 'abc\nGO\nSELECT 2", []msBatch{b("SELECT 'abc\nGO\nSELECT 2")}},
		{"procedure", "CREATE PROCEDURE p AS\nBEGIN\n  SELECT 1;\n\n  SELECT 2;\nEND\nGO\nEXEC p", []msBatch{b("CREATE PROCEDURE p AS\nBEGIN\n  SELECT 1;\n\n  SELECT 2;\nEND"), b("EXEC p")}},
		{"GO at end without newline", "SELECT 1\nGO 2", []msBatch{{"SELECT 1", 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mssqlBatches(t, tt.src)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestMSSQLSplitLinesAndEnd(t *testing.T) {
	src := "-- header\n\nSELECT 1\nGO 2 -- twice\n\n\nUPDATE t\n  SET a = 1\ngo\n"
	stmts, err := NewMSSQL().Split(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 2 {
		t.Fatalf("got %d batches", len(stmts))
	}
	if stmts[0].Line != 1 || stmts[0].Text != "-- header\n\nSELECT 1" || stmts[0].Count != 2 {
		t.Errorf("batch 0 = %+v", stmts[0])
	}
	if want := strings.Index(src, "\n\n\nUPDATE"); stmts[0].End != want {
		t.Errorf("batch 0 End = %d, want %d (end of the GO line)", stmts[0].End, want)
	}
	if stmts[1].Line != 7 || stmts[1].Count != 1 {
		t.Errorf("batch 1 = %+v", stmts[1])
	}
}

func TestMSSQLSplitBadCount(t *testing.T) {
	for _, src := range []string{"SELECT 1\nGO 0\n", "SELECT 1\nGO 99999999999\n"} {
		_, err := NewMSSQL().Split(src)
		if err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%q: err = %v, want line 2 error", src, err)
		}
	}
}

func TestMSSQLTokenize(t *testing.T) {
	src := "SELECT N'a''b', [x]]y], \"q\"\"r\", @v, @@SPID, #t, ##g, 0x1F, 1.5e3, $action -- c\n/* a /* b */ c */ x<>y;\nGO 2"
	type tk struct {
		Kind TokenKind
		Text string
	}
	var got []tk
	toks := NewMSSQL().Tokenize(src)
	end := 0
	for _, t := range toks {
		if t.Start != end {
			break
		}
		end = t.End
		if t.Kind != TokWhitespace {
			got = append(got, tk{t.Kind, t.Text})
		}
	}
	if end != len(src) {
		t.Fatalf("tokens not contiguous: covered %d of %d", end, len(src))
	}
	want := []tk{
		{TokIdent, "SELECT"}, {TokString, "N'a''b'"}, {TokPunct, ","}, {TokQuotedIdent, "[x]]y]"}, {TokPunct, ","},
		{TokQuotedIdent, `"q""r"`}, {TokPunct, ","}, {TokParam, "@v"}, {TokPunct, ","}, {TokIdent, "@@SPID"}, {TokPunct, ","},
		{TokIdent, "#t"}, {TokPunct, ","}, {TokIdent, "##g"}, {TokPunct, ","}, {TokNumber, "0x1F"}, {TokPunct, ","},
		{TokNumber, "1.5e3"}, {TokPunct, ","}, {TokIdent, "$action"}, {TokComment, "-- c"},
		{TokComment, "/* a /* b */ c */"}, {TokIdent, "x"}, {TokPunct, "<>"}, {TokIdent, "y"}, {TokPunct, ";"},
		{TokDelimiter, "GO 2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	if kw := Keywords(NewMSSQL(), "/* x */ select @a = 1", 0); !reflect.DeepEqual(kw, []string{"SELECT"}) {
		t.Errorf("Keywords = %v", kw)
	}
}

// at returns StatementAt for the position of marker "|" (removed from src).
func msAt(t *testing.T, m *MSSQL, marked string) (Statement, string) {
	t.Helper()
	pos := strings.Index(marked, "|")
	if pos < 0 {
		t.Fatal("no | marker")
	}
	src := marked[:pos] + marked[pos+1:]
	st, ok := m.StatementAt(src, pos)
	if !ok {
		t.Fatalf("StatementAt(%q, %d) found nothing", src, pos)
	}
	if got := src[st.Start : st.Start+len(st.Text)]; got != st.Text {
		t.Errorf("src[Start:] = %q, Text = %q", got, st.Text)
	}
	if want := 1 + strings.Count(src[:st.Start], "\n"); st.Line != want {
		t.Errorf("Line = %d, want %d", st.Line, want)
	}
	return st, src
}

func TestMSSQLStatementAt(t *testing.T) {
	tests := []struct {
		name, src, want string
		count           int
	}{
		{"semicolon first", "SEL|ECT 1; SELECT 2", "SELECT 1", 1},
		{"semicolon second", "SELECT 1; SEL|ECT 2", "SELECT 2", 1},
		{"after semicolon same line", "SELECT 1;| -- note\nSELECT 2", "SELECT 1", 1},
		{"remark not prepended", "SELECT 1; -- note\nSEL|ECT 2", "SELECT 2", 1},
		{"blank line", "SELECT 1\nFROM t\n\nSELECT| 2", "SELECT 2", 1},
		{"blank line first", "SELECT 1\nFR|OM t\n\nSELECT 2", "SELECT 1\nFROM t", 1},
		{"in blank region", "SELECT 1\n\n|\n\nSELECT 2", "SELECT 1", 1},
		{"before everything", "|\n\nSELECT 1; SELECT 2", "SELECT 1", 1},
		{"semicolon in string", "SELECT ';\n\n'| AS x; SELECT 2", "SELECT ';\n\n' AS x", 1},
		{"semicolon in comment", "SELECT 1 /* ; */ +| 1; SELECT 2", "SELECT 1 /* ; */ + 1", 1},
		{"parens keep blank lines", "SELECT *\nFROM (\n\n SELECT 1 AS a\n) x|", "SELECT *\nFROM (\n\n SELECT 1 AS a\n) x", 1},
		{"begin end block", "IF 1 = 1\nBEGIN\n  SELECT 1;\n\n  SELECT| 2;\nEND\nSELECT 3", "IF 1 = 1\nBEGIN\n  SELECT 1;\n\n  SELECT 2;\nEND\nSELECT 3", 1},
		{"begin end then semicolon", "IF 1 = 1\nBEGIN\n  SELECT 1;\nEND;\nSELECT| 3", "SELECT 3", 1},
		{"begin tran is a statement", "BEGIN TRAN; UPD|ATE t SET a = 1; COMMIT", "UPDATE t SET a = 1", 1},
		{"try catch", "BEGIN TRY\n SELECT 1;\nEND TRY\nBEGIN CATCH\n SELECT 2;\nEND CATCH;\nSELECT| 3", "SELECT 3", 1},
		{"case end", "SELECT CASE WHEN a = 1 THEN 'x;'\n\nELSE 'y' END FROM t;| SELECT 2", "SELECT CASE WHEN a = 1 THEN 'x;'\n\nELSE 'y' END FROM t", 1},
		{"if else joined", "IF @a = 1 SELECT 1; ELSE SEL|ECT 2; SELECT 3", "IF @a = 1 SELECT 1; ELSE SELECT 2", 1},
		{"module whole", "CREATE PROCEDURE p AS\nSELECT 1;\n\nSEL|ECT 2;\nGO\nEXEC p", "CREATE PROCEDURE p AS\nSELECT 1;\n\nSELECT 2;", 1},
		{"create or alter module", "CREATE OR ALTER VIEW v AS SELECT 1 AS a; -- x\n\n|", "CREATE OR ALTER VIEW v AS SELECT 1 AS a; -- x", 1},
		{"other batch", "SELECT 1\nGO\nSELECT| 2; SELECT 3\nGO", "SELECT 2", 1},
		{"cursor on GO line", "SELECT 1; SELECT 2\nG|O 3\nSELECT 4", "SELECT 2", 1},
		{"single statement keeps count", "INSERT t DEFAULT VALUES|\nGO 3", "INSERT t DEFAULT VALUES", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _ := msAt(t, NewMSSQL(), tt.src)
			if st.Text != tt.want || st.Count != tt.count {
				t.Errorf("got %q (count %d), want %q (count %d)", st.Text, st.Count, tt.want, tt.count)
			}
		})
	}
}

func TestMSSQLStatementAtScopes(t *testing.T) {
	src := "SELECT 1\n\nSELECT| 2; SELECT 3\nGO 2"
	st, _ := msAt(t, &MSSQL{Cursor: CursorSemicolon}, src)
	if st.Text != "SELECT 1\n\nSELECT 2" {
		t.Errorf("semicolon scope: %q", st.Text)
	}
	st, _ = msAt(t, &MSSQL{Cursor: CursorBatch}, src)
	if st.Text != "SELECT 1\n\nSELECT 2; SELECT 3" || st.Count != 2 {
		t.Errorf("batch scope: %q count %d", st.Text, st.Count)
	}
	if _, ok := NewMSSQL().StatementAt("-- only\nGO\n", 3); ok {
		t.Error("comment-only script has a statement")
	}
}
