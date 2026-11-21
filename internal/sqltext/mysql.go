package sqltext

import "strings"

// MySQL is the tokenizer and statement splitter for MySQL and MariaDB
// scripts (spec 9.3). It implements Splitter and Tokenizer. The zero value
// is ready to use; it holds no state between calls.
//
// Lexical rules:
//
//   - The default delimiter is ";". A DELIMITER line changes it (see below).
//     The delimiter is matched case-sensitively wherever a token may start
//     and also inside bare identifiers, numbers and operators, exactly like
//     the mysql client (which matches it at every byte outside strings and
//     comments). Delimiters inside strings, quoted identifiers and comments
//     never split.
//   - '…' and "…" are strings with backslash escapes and doubled quotes;
//     X'…', B'…' and N'…' are strings too. `…` is a quoted identifier with
//     doubled backticks.
//   - Comments: "#" to end of line; "--" followed by whitespace, a control
//     character or end of input ("--x" is not a comment); "/* … */".
//   - "/*! … */", "/*!80000 … */" and MariaDB's "/*M! … */" are executable
//     comments: one TokExecComment token, treated as code, never split.
//   - "@var" and "@@var" are TokIdent, "?" is TokParam.
//
// DELIMITER handling (mysql client semantics):
//
//   - The keyword is case-insensitive and must be the first thing on its
//     line (leading spaces/tabs allowed), followed by whitespace or the end
//     of the line.
//   - It is recognised only while the current statement buffer holds no
//     code (only whitespace and comments). Inside an unfinished statement
//     the line is ordinary statement text. Comments pending before a
//     DELIMITER line are discarded with it.
//   - The argument follows the mysql client documentation: unquoted, it is
//     read up to the first space or the end of the line; quoted with ', " or
//     `, it is read up to the matching quote (doubled quote or backslash
//     escapes the quote). Anything after the argument is ignored.
//   - A DELIMITER line with no argument, or whose argument contains a
//     backslash (rejected by the mysql client), is consumed and leaves the
//     delimiter unchanged.
//   - The whole line is consumed and never appears in a statement.
//
// Statements:
//
//   - Text starts at the first non-whitespace token (comments included) and
//     ends at the last non-whitespace token before the delimiter, so inner
//     and trailing comments are kept. Statements without any code token are
//     dropped.
//   - Comments that start on the same line as the previous delimiter (for
//     example "SELECT 1; -- note") are treated as remarks on that statement
//     and are not prepended to the next one, so Line points at real code.
//   - Unterminated strings, quoted identifiers and comments extend to the
//     end of the input; the trailing text is still returned as a statement
//     and Split never returns an error (the server reports the problem).
type MySQL struct{}

// NewMySQL returns a MySQL/MariaDB splitter and tokenizer.
func NewMySQL() *MySQL { return &MySQL{} }

var (
	_ Splitter  = (*MySQL)(nil)
	_ Tokenizer = (*MySQL)(nil)
)

// Tokenize returns the full token stream of src. Tokens are contiguous and
// cover src completely. A DELIMITER directive line is emitted as a TokIdent
// for the keyword, TokWhitespace, a TokDelimiter for the new delimiter (or
// TokComment if the argument is invalid) and TokComment for ignored text
// after the argument.
func (m *MySQL) Tokenize(src string) []Token {
	toks := scanMySQL(src)
	out := make([]Token, len(toks))
	for i, t := range toks {
		out[i] = t.Token
	}
	return out
}

// Split returns all statements of src that contain code. The error is
// always nil.
func (m *MySQL) Split(src string) ([]Statement, error) {
	return splitMySQL(src), nil
}

// StatementAt returns the statement whose range [Start, End) contains pos.
// If pos lies between statements (whitespace, a DELIMITER line, a dropped
// comment-only chunk) the nearest preceding statement is returned, or the
// first following one if none precedes. ok is false if src has no
// statements.
func (m *MySQL) StatementAt(src string, pos int) (Statement, bool) {
	stmts := splitMySQL(src)
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

// splitMySQL groups the scanner's tokens into statements.
func splitMySQL(src string) []Statement {
	toks := scanMySQL(src)
	var out []Statement
	start, startLine, lastEnd := -1, 0, 0
	hasCode := false
	delimLine := 0 // line of the last delimiter token; 0 = none

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
	open := func(t Token) {
		if start < 0 {
			start, startLine = t.Start, t.Line
		}
		lastEnd = t.End
	}

	for _, t := range toks {
		if t.directive {
			// Only recognised while the buffer holds no code: discard
			// pending comments.
			start, hasCode, delimLine = -1, false, 0
			continue
		}
		switch t.Kind {
		case TokWhitespace:
		case TokDelimiter:
			flush(t.End)
			delimLine = t.Line
		case TokComment:
			if start < 0 && t.Line == delimLine {
				continue // remark on the previous statement's line
			}
			open(t.Token)
		default:
			open(t.Token)
			hasCode = true
		}
	}
	flush(-1)
	return out
}

const mySpaceChars = " \t\r\n\f\v"

type mysqlToken struct {
	Token
	directive bool // part of a DELIMITER line
}

type mysqlScanner struct {
	src     string
	pos     int
	line    int
	delim   string
	hasCode bool // current statement buffer contains code
	toks    []mysqlToken
}

func scanMySQL(src string) []mysqlToken {
	s := &mysqlScanner{src: src, line: 1, delim: ";"}
	for s.pos < len(src) {
		s.next()
	}
	return s.toks
}

func (s *mysqlScanner) emit(kind TokenKind, end int, directive bool) {
	t := Token{Kind: kind, Text: s.src[s.pos:end], Start: s.pos, End: end, Line: s.line}
	s.toks = append(s.toks, mysqlToken{Token: t, directive: directive})
	s.line += strings.Count(t.Text, "\n")
	s.pos = end
	if directive {
		return
	}
	switch kind {
	case TokWhitespace, TokComment:
	case TokDelimiter:
		s.hasCode = false
	default:
		s.hasCode = true
	}
}

func (s *mysqlScanner) atDelim(i int) bool {
	return strings.HasPrefix(s.src[i:], s.delim)
}

func (s *mysqlScanner) next() {
	src, i := s.src, s.pos
	c := src[i]
	if !s.hasCode && (c == 'd' || c == 'D') && myAtLineStart(src, i) && s.directive() {
		return
	}
	if s.atDelim(i) {
		s.emit(TokDelimiter, i+len(s.delim), false)
		return
	}
	var n byte
	if i+1 < len(src) {
		n = src[i+1]
	}
	switch {
	case myIsSpace(c):
		j := i + 1
		for j < len(src) && myIsSpace(src[j]) && !s.atDelim(j) {
			j++
		}
		s.emit(TokWhitespace, j, false)
	case c == '#':
		s.emit(TokComment, myLineEnd(src, i), false)
	case c == '-' && n == '-' && (i+2 == len(src) || src[i+2] <= ' '):
		s.emit(TokComment, myLineEnd(src, i), false)
	case c == '/' && n == '*':
		e := len(src)
		if k := strings.Index(src[i+2:], "*/"); k >= 0 {
			e = i + 2 + k + 2
		}
		kind := TokComment
		if strings.HasPrefix(src[i+2:], "!") || strings.HasPrefix(src[i+2:], "M!") {
			kind = TokExecComment
		}
		s.emit(kind, e, false)
	case c == '\'' || c == '"':
		s.emit(TokString, myScanQuoted(src, i, c, true), false)
	case c == '`':
		s.emit(TokQuotedIdent, myScanQuoted(src, i, '`', false), false)
	case n == '\'' && strings.IndexByte("xXbBnN", c) >= 0:
		s.emit(TokString, myScanQuoted(src, i+1, '\'', true), false)
	case c == '?':
		s.emit(TokParam, i+1, false)
	case c == '@':
		j := i + 1
		if j < len(src) && src[j] == '@' && !s.atDelim(j) {
			j++
		}
		k := s.identEnd(j)
		if k > j {
			s.emit(TokIdent, k, false)
		} else {
			s.emit(TokPunct, i+1, false)
		}
	case myIsDigit(c) || (c == '.' && myIsDigit(n) && !s.atDelim(i+1)):
		s.number()
	case myIsIdent(c):
		s.emit(TokIdent, s.identEnd(i), false)
	default:
		s.emit(TokPunct, s.punctEnd(i), false)
	}
}

func (s *mysqlScanner) identEnd(j int) int {
	for j < len(s.src) && myIsIdent(s.src[j]) && !s.atDelim(j) {
		j++
	}
	return j
}

// number scans a numeric literal. A digit run immediately followed by
// identifier characters is an identifier (MySQL allows "1abc").
func (s *mysqlScanner) number() {
	src, i := s.src, s.pos
	ok := func(j int) bool { return j < len(src) && !s.atDelim(j) }
	j := i
	if src[i] == '0' && ok(i+1) && (src[i+1] == 'x' || src[i+1] == 'X') && ok(i+2) && myIsHexDigit(src[i+2]) {
		j = i + 2
		for ok(j) && myIsHexDigit(src[j]) {
			j++
		}
	} else if src[i] == '0' && ok(i+1) && (src[i+1] == 'b' || src[i+1] == 'B') && ok(i+2) && (src[i+2] == '0' || src[i+2] == '1') {
		j = i + 2
		for ok(j) && (src[j] == '0' || src[j] == '1') {
			j++
		}
	} else {
		for ok(j) && myIsDigit(src[j]) {
			j++
		}
		if ok(j) && src[j] == '.' {
			j++
			for ok(j) && myIsDigit(src[j]) {
				j++
			}
		}
		if ok(j) && (src[j] == 'e' || src[j] == 'E') {
			k := j + 1
			if ok(k) && (src[k] == '+' || src[k] == '-') {
				k++
			}
			if ok(k) && myIsDigit(src[k]) {
				j = k
				for ok(j) && myIsDigit(src[j]) {
					j++
				}
			}
		}
	}
	if ok(j) && myIsIdent(src[j]) && !strings.Contains(src[i:j], ".") {
		s.emit(TokIdent, s.identEnd(j), false)
		return
	}
	s.emit(TokNumber, j, false)
}

var mysqlOps = []string{"<=>", "->>", "<=", ">=", "<>", "!=", ":=", "||", "&&", "<<", ">>", "->"}

func (s *mysqlScanner) punctEnd(i int) int {
	for _, op := range mysqlOps {
		if !strings.HasPrefix(s.src[i:], op) {
			continue
		}
		clean := true
		for k := i + 1; k < i+len(op); k++ {
			if s.atDelim(k) {
				clean = false
				break
			}
		}
		if clean {
			return i + len(op)
		}
	}
	return i + 1
}

// directive recognises a DELIMITER line at s.pos and consumes it up to (not
// including) the line break.
func (s *mysqlScanner) directive() bool {
	const kw = "delimiter"
	src, i := s.src, s.pos
	if len(src)-i < len(kw) || !strings.EqualFold(src[i:i+len(kw)], kw) {
		return false
	}
	eol := myLineEnd(src, i)
	k := i + len(kw)
	if k < eol && src[k] != ' ' && src[k] != '\t' {
		return false
	}
	s.emit(TokIdent, k, true)
	m := mySkipBlanks(src, k, eol)
	if m > k {
		s.emit(TokWhitespace, m, true)
	}
	if m == eol {
		return true // no argument: consumed, delimiter unchanged
	}
	var delim string
	argEnd := m
	if q := src[m]; q == '\'' || q == '"' || q == '`' {
		var b strings.Builder
		j := m + 1
		for j < eol {
			ch := src[j]
			if ch == '\\' && j+1 < eol {
				b.WriteByte(src[j+1])
				j += 2
				continue
			}
			if ch == q {
				if j+1 < eol && src[j+1] == q {
					b.WriteByte(q)
					j += 2
					continue
				}
				j++
				break
			}
			b.WriteByte(ch)
			j++
		}
		delim, argEnd = b.String(), j
	} else {
		for argEnd < eol && src[argEnd] != ' ' && src[argEnd] != '\t' {
			argEnd++
		}
		delim = src[m:argEnd]
	}
	if delim != "" && !strings.Contains(delim, `\`) {
		s.emit(TokDelimiter, argEnd, true)
		s.delim = delim
	} else {
		s.emit(TokComment, argEnd, true)
	}
	if argEnd < eol {
		r := mySkipBlanks(src, argEnd, eol)
		if r > argEnd {
			s.emit(TokWhitespace, r, true)
		}
		if r < eol {
			s.emit(TokComment, eol, true)
		}
	}
	return true
}

// myScanQuoted returns the end of the quoted token starting at src[i] == q.
// Unterminated tokens extend to the end of src.
func myScanQuoted(src string, i int, q byte, backslash bool) int {
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

// myLineEnd returns the offset of the line break ("\n" or "\r\n") at or after
// i, or len(src).
func myLineEnd(src string, i int) int {
	k := strings.IndexByte(src[i:], '\n')
	if k < 0 {
		return len(src)
	}
	e := i + k
	if e > i && src[e-1] == '\r' {
		e--
	}
	return e
}

// myAtLineStart reports whether only spaces and tabs precede i on its line.
func myAtLineStart(src string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch src[j] {
		case '\n':
			return true
		case ' ', '\t':
		default:
			return false
		}
	}
	return true
}

func mySkipBlanks(src string, i, end int) int {
	for i < end && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return i
}

func myIsSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func myIsDigit(c byte) bool { return c >= '0' && c <= '9' }

func myIsHexDigit(c byte) bool {
	return myIsDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// myIsIdent reports bytes allowed in bare MySQL identifiers. Bytes >= 0x80
// (UTF-8 multi-byte sequences) are accepted as a whole.
func myIsIdent(c byte) bool {
	return c == '_' || c == '$' || myIsDigit(c) || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}
