package sqltext

import (
	"fmt"
	"strconv"
	"strings"
)

// CursorScope selects what MSSQL.StatementAt returns (assumption A10).
type CursorScope int

const (
	// CursorStatement (default, A10 as written): within the GO batch at the
	// cursor, statements are bounded by ";" and by blank lines, outside
	// strings, comments, parentheses and BEGIN … END / CASE … END blocks.
	// A batch that defines a module (CREATE/ALTER PROCEDURE, FUNCTION,
	// TRIGGER, VIEW, SCHEMA, DEFAULT, RULE) is always returned whole,
	// because the server requires it to be alone in its batch. A statement
	// starting with ELSE is joined to the preceding one (IF … ; ELSE …).
	CursorStatement CursorScope = iota
	// CursorSemicolon is CursorStatement without blank-line boundaries.
	CursorSemicolon
	// CursorBatch returns the whole GO batch at the cursor.
	CursorBatch
)

// MSSQL is the tokenizer and batch splitter for Transact-SQL scripts (spec
// 9.3). It implements Splitter and Tokenizer and holds no state between
// calls; the zero value uses CursorStatement.
//
// Lexical rules:
//
//   - '…' and N'…' are strings with doubled quotes (no backslash escapes).
//   - […] (with ]] for a literal "]") and "…" (with "") are quoted
//     identifiers (QUOTED_IDENTIFIER ON, the driver default).
//   - Comments: "--" to the end of the line, "/* … */" with nesting.
//   - "@name" is TokParam (variables and @pN parameters), "@@name" is
//     TokIdent (system functions), "#tmp" and "##tmp" are TokIdent.
//   - ";" is TokPunct: it ends a statement but never a batch.
//   - Numbers include 0x… binary literals.
//
// Batches:
//
//   - A GO line separates batches: optional spaces or tabs, the keyword GO
//     (case-insensitive), optionally whitespace and a repeat count, then
//     optional whitespace and an optional "--" comment up to the end of the
//     line. Lines inside strings, quoted identifiers and comments are never
//     GO lines. "GO;" or "GO x" are not GO lines (the server then reports a
//     syntax error, as sqlcmd would).
//   - The GO keyword and count are one TokDelimiter token (Text "GO 3");
//     Statement.Count is the count (1 without one). "GO 0" or a count that
//     does not fit in an int32 makes Split fail with the line number.
//   - Each batch is one Statement: from the first non-whitespace token
//     (comments included) to the last one before the GO line. Batches
//     without code are dropped. End includes the GO line.
//   - Unterminated strings, identifiers and comments extend to the end of
//     the input; the text is still returned (the server reports it).
type MSSQL struct {
	Cursor CursorScope
}

// NewMSSQL returns a T-SQL splitter and tokenizer with the A10 default.
func NewMSSQL() *MSSQL { return &MSSQL{} }

var (
	_ Splitter  = (*MSSQL)(nil)
	_ Tokenizer = (*MSSQL)(nil)
)

// Tokenize returns the full token stream of src; tokens are contiguous.
func (m *MSSQL) Tokenize(src string) []Token {
	toks, _ := scanMSSQL(src)
	out := make([]Token, len(toks))
	for i, t := range toks {
		out[i] = t.Token
	}
	return out
}

// Split returns the GO batches of src that contain code.
func (m *MSSQL) Split(src string) ([]Statement, error) {
	toks, err := scanMSSQL(src)
	if err != nil {
		return nil, err
	}
	return splitMSSQL(src, toks), nil
}

// StatementAt returns the statement at byte offset pos according to
// m.Cursor (see CursorScope). If pos lies between statements, the nearest
// preceding statement is returned, or the first following one if none
// precedes. Count is the batch's GO count when the result is the whole
// batch, else 1. Invalid GO counts are ignored here (Split reports them).
func (m *MSSQL) StatementAt(src string, pos int) (Statement, bool) {
	toks, _ := scanMSSQL(src)
	batches := splitMSSQL(src, toks)
	b, ok := pick(batches, pos)
	if !ok || m.Cursor == CursorBatch {
		return b, ok
	}
	subs := subStatements(src, toks, b, m.Cursor == CursorStatement)
	if len(subs) <= 1 {
		return b, true
	}
	return pick(subs, pos)
}

// pick returns the statement whose [Start, End) contains pos, else the
// nearest preceding one, else the first.
func pick(stmts []Statement, pos int) (Statement, bool) {
	if len(stmts) == 0 {
		return Statement{}, false
	}
	prev := -1
	for i, s := range stmts {
		if pos >= s.Start && pos < s.End {
			return s, true
		}
		if s.End <= pos {
			prev = i
		}
	}
	if prev >= 0 {
		return stmts[prev], true
	}
	return stmts[0], true
}

type msToken struct {
	Token
	directive bool // part of a GO line
	count     int  // GO count (TokDelimiter only)
}

func splitMSSQL(src string, toks []msToken) []Statement {
	var out []Statement
	start, startLine, lastEnd := -1, 0, 0
	hasCode := false
	count := 1
	flush := func(end int) {
		if hasCode {
			text := strings.TrimRight(src[start:lastEnd], mySpaceChars)
			if end < 0 {
				end = start + len(text)
			}
			out = append(out, Statement{Text: text, Line: startLine, Start: start, End: end, Count: count})
		}
		start, hasCode, count = -1, false, 1
	}
	for i, t := range toks {
		if t.directive {
			if t.Kind == TokDelimiter {
				count = t.count
				// End of the GO line: after its trailing blanks and comment.
				end := t.End
				for j := i + 1; j < len(toks) && toks[j].directive && toks[j].Kind != TokDelimiter; j++ {
					end = toks[j].End
				}
				flush(end)
			}
			continue
		}
		switch t.Kind {
		case TokWhitespace:
		case TokComment:
			if start < 0 {
				start, startLine = t.Start, t.Line
			}
			lastEnd = t.End
		default:
			if start < 0 {
				start, startLine = t.Start, t.Line
			}
			lastEnd = t.End
			hasCode = true
		}
	}
	flush(-1)
	return out
}

// moduleKinds are the objects whose CREATE must be alone in a batch.
var moduleKinds = map[string]bool{
	"PROC": true, "PROCEDURE": true, "FUNCTION": true, "TRIGGER": true, "VIEW": true,
	"SCHEMA": true, "DEFAULT": true, "RULE": true,
}

// beginNotBlock lists words after BEGIN that make it a statement, not a block.
var beginNotBlock = map[string]bool{
	"TRAN": true, "TRANSACTION": true, "DISTRIBUTED": true, "DIALOG": true, "CONVERSATION": true,
}

// subStatements splits batch b into statements at ";" and, with blank,
// at blank lines, at nesting depth 0 (see CursorStatement).
func subStatements(src string, toks []msToken, b Statement, blank bool) []Statement {
	textEnd := b.Start + len(b.Text)
	var code []msToken // code tokens of the batch
	first, last := -1, -1
	for i, t := range toks {
		if t.Start < b.Start || t.Start >= textEnd {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
		if t.Kind != TokWhitespace && t.Kind != TokComment {
			code = append(code, t)
		}
	}
	if first < 0 || len(code) == 0 {
		return nil
	}
	kw := func(i int) string {
		if i < len(code) && code[i].Kind == TokIdent {
			return strings.ToUpper(code[i].Text)
		}
		return ""
	}
	// Module batches stay whole.
	k := 1
	if kw(0) == "CREATE" && kw(1) == "OR" && kw(2) == "ALTER" {
		k = 3
	}
	if (kw(0) == "CREATE" || kw(0) == "ALTER") && moduleKinds[kw(k)] {
		return nil
	}

	var out []Statement
	start, startLine, lastEnd := -1, 0, 0
	hasCode := false
	sepLine := 0 // line of the last ";" boundary
	flush := func(end int) {
		if hasCode {
			text := strings.TrimRight(src[start:lastEnd], mySpaceChars)
			if end < 0 {
				end = start + len(text)
			}
			out = append(out, Statement{Text: text, Line: startLine, Start: start, End: end, Count: 1})
		}
		start, hasCode = -1, false
	}
	parens, blocks := 0, 0
	ci := 0 // index into code of the next code token
	for i := first; i <= last; i++ {
		t := toks[i]
		switch t.Kind {
		case TokWhitespace:
			if blank && hasCode && parens == 0 && blocks == 0 && strings.Count(t.Text, "\n") >= 2 {
				flush(-1)
			}
			continue
		case TokComment:
			if start < 0 && t.Line == sepLine {
				continue // remark on the previous statement's line
			}
			if start < 0 {
				start, startLine = t.Start, t.Line
			}
			lastEnd = t.End
			continue
		}
		word := kw(ci)
		ci++
		if t.Kind == TokPunct && t.Text == ";" && parens == 0 && blocks == 0 {
			flush(t.End)
			sepLine = t.Line
			continue
		}
		if start < 0 {
			start, startLine = t.Start, t.Line
		}
		lastEnd = t.End
		hasCode = true
		switch {
		case t.Kind == TokPunct && t.Text == "(":
			parens++
		case t.Kind == TokPunct && t.Text == ")":
			if parens > 0 {
				parens--
			}
		case word == "BEGIN" && !beginNotBlock[kw(ci)]:
			blocks++
		case word == "CASE":
			blocks++
		case word == "END" && kw(ci) != "CONVERSATION":
			if blocks > 0 {
				blocks--
			}
		}
	}
	flush(-1)
	if len(out) > 0 {
		out[len(out)-1].End = b.End
	}
	// Join "ELSE …" to the preceding IF statement.
	joined := out[:0]
	for _, s := range out {
		if n := len(joined); n > 0 && startsWithWord(s.Text, "ELSE") {
			p := &joined[n-1]
			p.Text = src[p.Start : s.Start+len(s.Text)]
			p.End = s.End
			continue
		}
		joined = append(joined, s)
	}
	if len(joined) == 1 {
		joined[0].Count = b.Count
	}
	return joined
}

func startsWithWord(s, w string) bool {
	if len(s) < len(w) || !strings.EqualFold(s[:len(w)], w) {
		return false
	}
	return len(s) == len(w) || !msIsIdent(s[len(w)])
}

type msScanner struct {
	src  string
	pos  int
	line int
	toks []msToken
	err  error
}

func scanMSSQL(src string) ([]msToken, error) {
	s := &msScanner{src: src, line: 1}
	for s.pos < len(src) {
		s.next()
	}
	return s.toks, s.err
}

func (s *msScanner) emit(kind TokenKind, end int, directive bool, count int) {
	t := Token{Kind: kind, Text: s.src[s.pos:end], Start: s.pos, End: end, Line: s.line}
	s.toks = append(s.toks, msToken{Token: t, directive: directive, count: count})
	s.line += strings.Count(t.Text, "\n")
	s.pos = end
}

func (s *msScanner) next() {
	src, i := s.src, s.pos
	c := src[i]
	if (c == 'g' || c == 'G') && myAtLineStart(src, i) && s.goLine() {
		return
	}
	var n byte
	if i+1 < len(src) {
		n = src[i+1]
	}
	switch {
	case myIsSpace(c):
		j := i + 1
		for j < len(src) && myIsSpace(src[j]) {
			j++
		}
		s.emit(TokWhitespace, j, false, 0)
	case c == '-' && n == '-':
		s.emit(TokComment, myLineEnd(src, i), false, 0)
	case c == '/' && n == '*':
		s.emit(TokComment, msBlockCommentEnd(src, i), false, 0)
	case c == '\'':
		s.emit(TokString, msScanQuoted(src, i, '\''), false, 0)
	case (c == 'n' || c == 'N') && n == '\'':
		s.emit(TokString, msScanQuoted(src, i+1, '\''), false, 0)
	case c == '"':
		s.emit(TokQuotedIdent, msScanQuoted(src, i, '"'), false, 0)
	case c == '[':
		s.emit(TokQuotedIdent, msScanQuoted(src, i, ']'), false, 0)
	case c == '@':
		if n == '@' {
			if k := msIdentEnd(src, i+2); k > i+2 {
				s.emit(TokIdent, k, false, 0)
				return
			}
		} else if k := msIdentEnd(src, i+1); k > i+1 {
			s.emit(TokParam, k, false, 0)
			return
		}
		s.emit(TokPunct, i+1, false, 0)
	case c == '#' || c == '$':
		j := i + 1
		if c == '#' && n == '#' {
			j++
		}
		if j < len(src) && msIsIdentStart(src[j]) {
			s.emit(TokIdent, msIdentEnd(src, j), false, 0)
			return
		}
		s.emit(TokPunct, i+1, false, 0)
	case myIsDigit(c) || (c == '.' && myIsDigit(n)):
		s.emit(TokNumber, msNumberEnd(src, i), false, 0)
	case msIsIdentStart(c):
		s.emit(TokIdent, msIdentEnd(src, i), false, 0)
	default:
		s.emit(TokPunct, msPunctEnd(src, i), false, 0)
	}
}

// goLine recognises a GO line at s.pos and consumes it up to (not
// including) the line break.
func (s *msScanner) goLine() bool {
	src, i := s.src, s.pos
	if len(src)-i < 2 || !strings.EqualFold(src[i:i+2], "go") {
		return false
	}
	eol := myLineEnd(src, i)
	k := i + 2
	m := mySkipBlanks(src, k, eol)
	d := m
	for d < eol && myIsDigit(src[d]) {
		d++
	}
	if d > m && m == k {
		return false // "GO3"
	}
	r := mySkipBlanks(src, d, eol)
	if r < eol && !(r+1 < eol && src[r] == '-' && src[r+1] == '-') {
		return false
	}
	count := 1
	if d > m {
		n, err := strconv.ParseInt(src[m:d], 10, 32)
		if err != nil || n < 1 {
			if s.err == nil {
				s.err = fmt.Errorf("line %d: invalid GO count %q (want 1 to 2147483647)", s.line, src[m:d])
			}
			n = 1
		}
		count = int(n)
	} else {
		d = k
	}
	s.emit(TokDelimiter, d, true, count)
	if r > d {
		s.emit(TokWhitespace, r, true, 0)
	}
	if r < eol {
		s.emit(TokComment, eol, true, 0)
	}
	return true
}

func msBlockCommentEnd(src string, i int) int {
	depth := 0
	j := i
	for j+1 < len(src) {
		switch {
		case src[j] == '/' && src[j+1] == '*':
			depth++
			j += 2
		case src[j] == '*' && src[j+1] == '/':
			depth--
			j += 2
			if depth == 0 {
				return j
			}
		default:
			j++
		}
	}
	return len(src)
}

// msScanQuoted returns the end of the token starting at src[i] (the opening
// quote) and closed by q; a doubled q is an escaped q.
func msScanQuoted(src string, i int, q byte) int {
	j := i + 1
	for j < len(src) {
		if src[j] == q {
			if j+1 < len(src) && src[j+1] == q {
				j += 2
				continue
			}
			return j + 1
		}
		j++
	}
	return len(src)
}

func msNumberEnd(src string, i int) int {
	j := i
	if src[i] == '0' && i+1 < len(src) && (src[i+1] == 'x' || src[i+1] == 'X') {
		j = i + 2
		for j < len(src) && myIsHexDigit(src[j]) {
			j++
		}
		return j
	}
	for j < len(src) && myIsDigit(src[j]) {
		j++
	}
	if j < len(src) && src[j] == '.' {
		j++
		for j < len(src) && myIsDigit(src[j]) {
			j++
		}
	}
	if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
		k := j + 1
		if k < len(src) && (src[k] == '+' || src[k] == '-') {
			k++
		}
		if k < len(src) && myIsDigit(src[k]) {
			j = k
			for j < len(src) && myIsDigit(src[j]) {
				j++
			}
		}
	}
	return j
}

var mssqlOps = []string{"<=", ">=", "<>", "!=", "!<", "!>", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "::"}

func msPunctEnd(src string, i int) int {
	for _, op := range mssqlOps {
		if strings.HasPrefix(src[i:], op) {
			return i + len(op)
		}
	}
	return i + 1
}

func msIsIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func msIsIdent(c byte) bool {
	return msIsIdentStart(c) || myIsDigit(c) || c == '@' || c == '#' || c == '$'
}

func msIdentEnd(src string, j int) int {
	for j < len(src) && msIsIdent(src[j]) {
		j++
	}
	return j
}
