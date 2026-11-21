package sqltext

import "strings"

// Postgres is the tokenizer and statement splitter for PostgreSQL scripts
// (spec 9.3). It implements Splitter and Tokenizer. The zero value is ready
// to use; it holds no state between calls.
//
// Lexical rules (as the PostgreSQL server and psql lex them):
//
//   - ";" is the statement delimiter. Like psql, a ";" inside parentheses
//     (CREATE RULE … DO (…; …)) or inside a SQL-standard function body
//     (CREATE [OR REPLACE] FUNCTION|PROCEDURE … BEGIN ATOMIC …; … END) does
//     not split; such a ";" is emitted as TokPunct.
//   - '…' is a string with doubled quotes; backslashes are literal
//     (standard_conforming_strings = on, the default since 9.1).
//   - E'…' (and e'…') is a string with backslash escapes and doubled quotes.
//     B'…', X'…', N'…' and U&'…' are strings with doubled quotes.
//   - $tag$…$tag$ and $$…$$ are dollar-quoted strings; tag follows the
//     identifier rules without "$". Nothing inside is interpreted.
//   - "…" (and U&"…") is a quoted identifier with doubled quotes.
//   - Comments: "--" to end of line; "/* … */", which nests.
//   - $1, $2, … are TokParam. A lone "?" operator is TokParam too, so that
//     callers can rebind "?" placeholders (it is also the jsonb "exists"
//     operator; "?|" and "?&" are operators).
//   - Operators are maximal runs of + - * / < > = ~ ! @ # % ^ & | ` ?,
//     cut before "--" and "/*" as the server does; "::" is one token.
//   - psql meta-commands (\set, \i, …) and psql variables (:var) are not
//     supported.
//
// Statements follow the same rules as the MySQL splitter: text starts at
// the first non-whitespace token (comments included) and ends at the last
// non-whitespace token before the delimiter; comments on the line of the
// previous delimiter belong to that statement; statements without code are
// dropped; unterminated strings, identifiers and comments extend to the end
// of the input and Split never returns an error.
type Postgres struct{}

// NewPostgres returns a PostgreSQL splitter and tokenizer.
func NewPostgres() *Postgres { return &Postgres{} }

var (
	_ Splitter  = (*Postgres)(nil)
	_ Tokenizer = (*Postgres)(nil)
)

// Tokenize returns the full token stream of src. Tokens are contiguous and
// cover src completely.
func (p *Postgres) Tokenize(src string) []Token { return scanPostgres(src) }

// Split returns all statements of src that contain code. The error is
// always nil.
func (p *Postgres) Split(src string) ([]Statement, error) {
	return splitPostgres(src), nil
}

// StatementAt returns the statement whose range [Start, End) contains pos.
// If pos lies between statements the nearest preceding statement is
// returned, or the first following one if none precedes. ok is false if src
// has no statements.
func (p *Postgres) StatementAt(src string, pos int) (Statement, bool) {
	stmts := splitPostgres(src)
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

func splitPostgres(src string) []Statement {
	toks := scanPostgres(src)
	var out []Statement
	start, startLine, lastEnd := -1, 0, 0
	hasCode := false
	delimLine := 0

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
	for _, t := range toks {
		switch t.Kind {
		case TokWhitespace:
		case TokDelimiter:
			flush(t.End)
			delimLine = t.Line
		case TokComment:
			if start < 0 && t.Line == delimLine {
				continue
			}
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

type pgScanner struct {
	src  string
	pos  int
	line int
	toks []Token

	// psql-style statement state: parenthesis depth, BEGIN/CASE … END
	// depth inside CREATE FUNCTION/PROCEDURE, and the lower-cased first
	// four identifiers of the current statement.
	parens int
	begins int
	idents []string
}

func scanPostgres(src string) []Token {
	s := &pgScanner{src: src, line: 1}
	for s.pos < len(src) {
		s.next()
	}
	return s.toks
}

func (s *pgScanner) emit(kind TokenKind, end int) {
	t := Token{Kind: kind, Text: s.src[s.pos:end], Start: s.pos, End: end, Line: s.line}
	s.toks = append(s.toks, t)
	s.line += strings.Count(t.Text, "\n")
	s.pos = end
}

func (s *pgScanner) next() {
	src, i := s.src, s.pos
	c := src[i]
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
		s.emit(TokWhitespace, j)
	case c == '-' && n == '-':
		s.emit(TokComment, myLineEnd(src, i))
	case c == '/' && n == '*':
		s.emit(TokComment, pgBlockCommentEnd(src, i))
	case c == '\'':
		s.code()
		s.emit(TokString, pgScanQuoted(src, i, '\'', false))
	case c == '"':
		s.code()
		s.emit(TokQuotedIdent, pgScanQuoted(src, i, '"', false))
	case (c == 'e' || c == 'E') && n == '\'':
		s.code()
		s.emit(TokString, pgScanQuoted(src, i+1, '\'', true))
	case n == '\'' && strings.IndexByte("bBxXnN", c) >= 0:
		s.code()
		s.emit(TokString, pgScanQuoted(src, i+1, '\'', false))
	case (c == 'u' || c == 'U') && n == '&' && i+2 < len(src) && (src[i+2] == '\'' || src[i+2] == '"'):
		s.code()
		kind := TokString
		if src[i+2] == '"' {
			kind = TokQuotedIdent
		}
		s.emit(kind, pgScanQuoted(src, i+2, src[i+2], false))
	case c == '$' && myIsDigit(n):
		j := i + 1
		for j < len(src) && myIsDigit(src[j]) {
			j++
		}
		s.code()
		s.emit(TokParam, j)
	case c == '$':
		if tag, ok := pgDollarTag(src, i); ok {
			s.code()
			e := strings.Index(src[i+len(tag):], tag)
			end := len(src)
			if e >= 0 {
				end = i + len(tag) + e + len(tag)
			}
			s.emit(TokString, end)
			return
		}
		s.code()
		s.emit(TokPunct, i+1)
	case myIsDigit(c) || (c == '.' && myIsDigit(n)):
		s.code()
		s.emit(TokNumber, pgNumberEnd(src, i))
	case pgIsIdentStart(c):
		j := i + 1
		for j < len(src) && pgIsIdent(src[j]) {
			j++
		}
		s.ident(src[i:j])
		s.emit(TokIdent, j)
	case c == ';':
		if s.parens > 0 || s.begins > 0 {
			s.emit(TokPunct, i+1)
			return
		}
		s.emit(TokDelimiter, i+1)
		s.parens, s.begins, s.idents = 0, 0, s.idents[:0]
	case c == '(':
		s.parens++
		s.code()
		s.emit(TokPunct, i+1)
	case c == ')':
		if s.parens > 0 {
			s.parens--
		}
		s.code()
		s.emit(TokPunct, i+1)
	case c == ':' && n == ':':
		s.code()
		s.emit(TokPunct, i+2)
	case strings.IndexByte(pgOpChars, c) >= 0:
		j := i + 1
		for j < len(src) && strings.IndexByte(pgOpChars, src[j]) >= 0 {
			if (src[j] == '-' && j+1 < len(src) && src[j+1] == '-') || (src[j] == '/' && j+1 < len(src) && src[j+1] == '*') {
				break
			}
			j++
		}
		s.code()
		if j == i+1 && c == '?' {
			s.emit(TokParam, j)
			return
		}
		s.emit(TokPunct, j)
	default:
		s.code()
		s.emit(TokPunct, i+1)
	}
}

const pgOpChars = "+-*/<>=~!@#%^&|`?"

// code records a non-identifier code token for the first-identifiers
// tracking (psql only looks at the leading identifiers of a statement).
func (s *pgScanner) code() {
	if len(s.idents) < 4 {
		s.idents = append(s.idents, "")
	}
}

// ident tracks BEGIN/CASE … END nesting inside CREATE FUNCTION/PROCEDURE
// (SQL-standard bodies, PostgreSQL 14+), the way psql does.
func (s *pgScanner) ident(word string) {
	w := strings.ToLower(word)
	if len(s.idents) < 4 {
		s.idents = append(s.idents, w)
	}
	switch w {
	case "begin", "case":
		if s.inRoutine() {
			s.begins++
		}
	case "end":
		if s.begins > 0 {
			s.begins--
		}
	}
}

func (s *pgScanner) inRoutine() bool {
	id := s.idents
	at := func(i int, w string) bool { return len(id) > i && id[i] == w }
	routine := func(i int) bool { return at(i, "function") || at(i, "procedure") }
	return at(0, "create") && (routine(1) || (at(1, "or") && at(2, "replace") && routine(3)))
}

// pgDollarTag returns the opening tag ($$ or $tag$) at src[i] == '$'.
func pgDollarTag(src string, i int) (string, bool) {
	j := i + 1
	if j < len(src) && src[j] == '$' {
		return "$$", true
	}
	if j >= len(src) || !pgIsIdentStart(src[j]) {
		return "", false
	}
	for j < len(src) && pgIsIdentStart(src[j]) || (j < len(src) && myIsDigit(src[j])) {
		j++
	}
	if j < len(src) && src[j] == '$' {
		return src[i : j+1], true
	}
	return "", false
}

// pgBlockCommentEnd returns the end of a nested /* … */ comment at i.
func pgBlockCommentEnd(src string, i int) int {
	depth := 0
	j := i
	for j < len(src) {
		switch {
		case j+1 < len(src) && src[j] == '/' && src[j+1] == '*':
			depth++
			j += 2
		case j+1 < len(src) && src[j] == '*' && src[j+1] == '/':
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

// pgScanQuoted returns the end of the quoted token starting at src[i] == q.
// Doubled quotes escape the quote; with backslash, so does "\".
func pgScanQuoted(src string, i int, q byte, backslash bool) int {
	j := i + 1
	for j < len(src) {
		ch := src[j]
		if backslash && ch == '\\' {
			j += 2
			continue
		}
		if ch == q {
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

// pgNumberEnd scans a numeric literal: decimal with optional fraction and
// exponent, 0x/0o/0b integers and "_" digit separators (PostgreSQL 16).
func pgNumberEnd(src string, i int) int {
	digits := func(j int, ok func(byte) bool) int {
		for j < len(src) && (ok(src[j]) || (src[j] == '_' && j+1 < len(src) && ok(src[j+1]))) {
			j++
		}
		return j
	}
	if src[i] == '0' && i+2 < len(src) {
		switch src[i+1] {
		case 'x', 'X':
			if myIsHexDigit(src[i+2]) {
				return digits(i+2, myIsHexDigit)
			}
		case 'o', 'O':
			if src[i+2] >= '0' && src[i+2] <= '7' {
				return digits(i+2, func(b byte) bool { return b >= '0' && b <= '7' })
			}
		case 'b', 'B':
			if src[i+2] == '0' || src[i+2] == '1' {
				return digits(i+2, func(b byte) bool { return b == '0' || b == '1' })
			}
		}
	}
	j := digits(i, myIsDigit)
	// ".." is not part of a number (array slices: a[1..2] is not valid, but
	// be safe for ranges in other contexts).
	if j < len(src) && src[j] == '.' && !(j+1 < len(src) && src[j+1] == '.') {
		j = digits(j+1, myIsDigit)
	}
	if j < len(src) && (src[j] == 'e' || src[j] == 'E') {
		k := j + 1
		if k < len(src) && (src[k] == '+' || src[k] == '-') {
			k++
		}
		if k < len(src) && myIsDigit(src[k]) {
			j = digits(k, myIsDigit)
		}
	}
	return j
}

func pgIsIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func pgIsIdent(c byte) bool { return pgIsIdentStart(c) || myIsDigit(c) || c == '$' }
