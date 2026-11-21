package sqltext

import "strings"

// CompletionKind is the kind of candidates the completion context asks for
// (spec 9.2).
type CompletionKind int

const (
	// CompleteNone: no completion (inside a string or comment, after a
	// number, …).
	CompleteNone CompletionKind = iota
	// CompleteTables: after FROM, JOIN, INTO, UPDATE, TABLE, DESCRIBE (and
	// in a comma-separated FROM list): tables and views of the current
	// database or schema, then databases (and schemas).
	CompleteTables
	// CompleteObjects: "db." (or "schema.", "db.schema.") in a table
	// position: objects of the database/schema named by Qualifier.
	CompleteObjects
	// CompleteColumns: "alias." or "table." elsewhere: columns of Table.
	CompleteColumns
	// CompleteProcedures: after CALL, EXEC, EXECUTE.
	CompleteProcedures
	// CompleteAny: elsewhere inside a statement: columns of Tables, then
	// keywords and functions.
	CompleteAny
)

func (k CompletionKind) String() string {
	switch k {
	case CompleteTables:
		return "tables"
	case CompleteObjects:
		return "objects"
	case CompleteColumns:
		return "columns"
	case CompleteProcedures:
		return "procedures"
	case CompleteAny:
		return "any"
	}
	return "none"
}

// TableRef is a table named in a statement.
type TableRef struct {
	// Parts is the qualified name as written, unquoted: ["customers"],
	// ["shop","customers"] (MySQL db.table / PostgreSQL schema.table),
	// ["db","schema","table"]. Nil for a derived table (subquery).
	Parts []string
	// Alias is the correlation name ("" if none).
	Alias string
}

// Name returns the last part of the qualified name ("" for derived tables).
func (r TableRef) Name() string {
	if len(r.Parts) == 0 {
		return ""
	}
	return r.Parts[len(r.Parts)-1]
}

// Completion describes what to complete at the cursor.
type Completion struct {
	Kind CompletionKind
	// Prefix is the partial identifier before the cursor, unquoted.
	Prefix string
	// Start is the byte offset in src where the replacement begins; it
	// includes an opening quote. The text to replace is src[Start:cursor].
	Start int
	// Quote is the opening quote character if the prefix started with one
	// ('`', '"' or '['), 0 otherwise.
	Quote byte
	// Qualifier holds the unquoted identifiers before the dot(s) for
	// CompleteObjects and CompleteColumns ("shop." → ["shop"]).
	Qualifier []string
	// Table is the table whose columns CompleteColumns asks for. With
	// TableFound false the qualifier matched no table or alias of the
	// statement; Table.Parts is then the qualifier itself (it may name a
	// table directly, or a database: callers may fall back to objects).
	Table      *TableRef
	TableFound bool
	// Tables are the tables referenced anywhere in the current statement
	// (before and after the cursor), in order.
	Tables []TableRef
}

// CompletionAt computes the completion context at byte offset cursor of
// src using t. src may be a whole buffer: only the statement around the
// cursor (bounded by delimiter tokens) is considered, including the text
// after the cursor (for alias resolution). This is a token-level
// heuristic, no parser (spec 3.3).
func CompletionAt(t Tokenizer, src string, cursor int) Completion {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(src) {
		cursor = len(src)
	}
	toks := t.Tokenize(src)

	// Statement bounds: delimiter tokens around the cursor.
	lo, hi := 0, len(toks)
	for i, tk := range toks {
		if tk.Kind != TokDelimiter {
			continue
		}
		if tk.End <= cursor {
			lo = i + 1
		} else if tk.Start >= cursor {
			hi = i
			break
		} else {
			// Cursor inside a multi-byte delimiter: nothing to complete.
			return Completion{Kind: CompleteNone, Start: cursor}
		}
	}
	stmt := toks[lo:hi]

	c := Completion{Start: cursor}
	cur := -1 // index in stmt of the token holding the prefix
scan:
	for i, tk := range stmt {
		if tk.Start < cursor && cursor <= tk.End {
			switch tk.Kind {
			case TokString, TokComment, TokExecComment:
				if cursor < tk.End || unterminated(tk) {
					return Completion{Kind: CompleteNone, Start: cursor}
				}
			case TokNumber, TokParam:
				return Completion{Kind: CompleteNone, Start: cursor}
			case TokIdent:
				if strings.HasPrefix(tk.Text, "@") {
					return Completion{Kind: CompleteNone, Start: cursor}
				}
				cur = i
				c.Start = tk.Start
				c.Prefix = src[tk.Start:cursor]
			case TokQuotedIdent:
				if tk.End == len(src) && cursor < len(src) && !closedQuoted(tk.Text) {
					// An opening quote just typed swallows the rest of the
					// input: close it at the cursor and look again, so the
					// statement after the cursor still counts.
					return CompletionAt(t, src[:cursor]+string(closingQuote(tk.Text[0]))+src[cursor:], cursor)
				}
				cur = i
				c.Start = tk.Start
				c.Quote = tk.Text[0]
				c.Prefix = unquoteIdent(src[tk.Start:cursor])
			}
			break scan
		}
	}

	// Significant tokens of the statement, without the prefix token.
	var sig []Token
	before := 0 // number of significant tokens before the prefix
	for i, tk := range stmt {
		if tk.Kind == TokWhitespace || tk.Kind == TokComment || i == cur {
			continue
		}
		sig = append(sig, tk)
		if tk.End <= c.Start {
			before = len(sig)
		}
	}
	c.Tables = tableRefs(sig)

	// Qualifier chain "a.b." directly before the prefix.
	j := before
	var qual []string
	for j > 0 && isPunct(sig[j-1], ".") {
		if j < 2 || !isName(sig[j-2]) {
			break
		}
		qual = append([]string{nameOf(sig[j-2])}, qual...)
		j -= 2
	}
	if j > 0 && isPunct(sig[j-1], ".") {
		// Something like "f(x)." — not a name chain.
		return Completion{Kind: CompleteNone, Start: c.Start}
	}
	c.Qualifier = qual

	tablePos := isTablePosition(sig[:j])
	procPos := j > 0 && isKeyword(sig[j-1], "CALL", "EXEC", "EXECUTE")
	switch {
	case procPos:
		c.Kind = CompleteProcedures
	case len(qual) > 0 && tablePos:
		c.Kind = CompleteObjects
	case len(qual) > 0:
		c.Kind = CompleteColumns
		c.Table, c.TableFound = resolveQualifier(c.Tables, qual)
	case tablePos:
		c.Kind = CompleteTables
	default:
		c.Kind = CompleteAny
	}
	return c
}

// resolveQualifier finds the table a qualifier refers to: an alias first,
// then a table whose qualified name ends with the qualifier.
func resolveQualifier(refs []TableRef, qual []string) (*TableRef, bool) {
	if len(qual) == 1 {
		for i := range refs {
			if refs[i].Alias != "" && strings.EqualFold(refs[i].Alias, qual[0]) {
				return &refs[i], true
			}
		}
	}
	for i := range refs {
		r := refs[i]
		if r.Alias != "" && len(qual) == 1 {
			// A table with an alias can still be referenced by its own
			// name in MySQL only when unaliased; accept it anyway as a
			// fallback below.
			continue
		}
		if suffixFold(r.Parts, qual) {
			return &refs[i], true
		}
	}
	for i := range refs {
		if suffixFold(refs[i].Parts, qual) {
			return &refs[i], true
		}
	}
	return &TableRef{Parts: append([]string(nil), qual...)}, false
}

func suffixFold(parts, suffix []string) bool {
	if len(parts) < len(suffix) || len(suffix) == 0 {
		return false
	}
	off := len(parts) - len(suffix)
	for i, s := range suffix {
		if !strings.EqualFold(parts[off+i], s) {
			return false
		}
	}
	return true
}

// tableKeywords introduce a table name.
var tableKeywords = map[string]bool{
	"FROM": true, "JOIN": true, "INTO": true, "UPDATE": true, "TABLE": true,
	"DESCRIBE": true, "STRAIGHT_JOIN": true,
}

// isTablePosition reports whether the next token after sig is a table
// name position.
func isTablePosition(sig []Token) bool {
	n := len(sig)
	if n == 0 {
		return false
	}
	last := sig[n-1]
	if last.Kind == TokIdent {
		k := strings.ToUpper(last.Text)
		if tableKeywords[k] {
			return true
		}
		// DESC as a statement (DESC t), not ORDER BY x DESC.
		if k == "DESC" && n == 1 {
			return true
		}
		// TRUNCATE t (without TABLE).
		if k == "TRUNCATE" && n == 1 {
			return true
		}
		return false
	}
	if isPunct(last, ",") {
		// Comma in a FROM list (or multi-table UPDATE / DELETE … FROM).
		k := clauseKeyword(sig[:n-1])
		return k == "FROM" || k == "UPDATE"
	}
	return false
}

// clauseStops are the keywords clauseKeyword looks for.
var clauseStops = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "GROUP": true, "ORDER": true,
	"HAVING": true, "LIMIT": true, "ON": true, "USING": true, "SET": true,
	"VALUES": true, "JOIN": true, "UPDATE": true, "INTO": true, "WINDOW": true,
	"UNION": true, "RETURNING": true, "BY": true,
}

// clauseKeyword returns the nearest clause keyword at the paren depth of
// the end of sig (upper case), or "".
func clauseKeyword(sig []Token) string {
	depth := 0
	for i := len(sig) - 1; i >= 0; i-- {
		tk := sig[i]
		switch {
		case isPunct(tk, ")"):
			depth++
		case isPunct(tk, "("):
			if depth == 0 {
				return ""
			}
			depth--
		case depth == 0 && tk.Kind == TokIdent:
			if k := strings.ToUpper(tk.Text); clauseStops[k] {
				return k
			}
		}
	}
	return ""
}

// aliasStops are words that can follow a table name but are not aliases.
var aliasStops = map[string]bool{
	"WHERE": true, "ON": true, "USING": true, "JOIN": true, "INNER": true,
	"LEFT": true, "RIGHT": true, "FULL": true, "OUTER": true, "CROSS": true,
	"NATURAL": true, "STRAIGHT_JOIN": true, "GROUP": true, "ORDER": true,
	"HAVING": true, "LIMIT": true, "OFFSET": true, "FETCH": true, "UNION": true,
	"EXCEPT": true, "INTERSECT": true, "MINUS": true, "WINDOW": true, "FOR": true,
	"LOCK": true, "SET": true, "VALUES": true, "VALUE": true, "SELECT": true,
	"PARTITION": true, "USE": true, "IGNORE": true, "FORCE": true, "INTO": true,
	"RETURNING": true, "WITH": true, "TABLESAMPLE": true, "PIVOT": true,
	"UNPIVOT": true, "OUTPUT": true, "DEFAULT": true, "AS": true, "FROM": true,
	"LATERAL": true, "ONLY": true, "DELETE": true, "UPDATE": true, "INSERT": true,
	"WHEN": true, "THEN": true, "ELSE": true, "END": true, "AND": true, "OR": true,
	"NOT": true, "IN": true, "IS": true, "LIKE": true, "BETWEEN": true,
	"CASCADE": true, "RESTRICT": true, "IF": true, "EXISTS": true,
	"ROWS": true, "ROW": true, "DO": true, "DUPLICATE": true, "KEY": true,
	"ADD": true, "DROP": true, "MODIFY": true, "CHANGE": true, "RENAME": true,
	"ALTER": true, "ENGINE": true, "COMMENT": true, "READ": true, "WRITE": true,
	"OUTFILE": true, "DUMPFILE": true, "WAIT": true, "NOWAIT": true,
}

// nameModifiers may stand between a table keyword and the name.
var nameModifiers = map[string]bool{
	"IF": true, "NOT": true, "EXISTS": true, "ONLY": true, "LATERAL": true, "IGNORE": true,
}

// tableRefs collects the tables named in the significant tokens of a
// statement: after FROM/JOIN/UPDATE/INTO/TABLE/DESCRIBE, comma lists after
// FROM and UPDATE, with optional [AS] alias. Derived tables "(…) AS x"
// become refs with nil Parts.
func tableRefs(sig []Token) []TableRef {
	var out []TableRef
	n := len(sig)
	for i := 0; i < n; i++ {
		tk := sig[i]
		if tk.Kind != TokIdent {
			continue
		}
		kw := strings.ToUpper(tk.Text)
		list := kw == "FROM" || kw == "UPDATE"
		if !tableKeywords[kw] && !(kw == "DESC" && i == 0) && !(kw == "TRUNCATE" && i == 0) {
			continue
		}
		j := i + 1
		for j < n {
			// Skip modifiers like IF NOT EXISTS, ONLY, LATERAL.
			for j < n && sig[j].Kind == TokIdent && nameModifiers[strings.ToUpper(sig[j].Text)] {
				j++
			}
			if j >= n {
				break
			}
			var ref TableRef
			switch {
			case isPunct(sig[j], "("):
				j = skipParens(sig, j)
			case isName(sig[j]) && !(sig[j].Kind == TokIdent && aliasStops[strings.ToUpper(sig[j].Text)]):
				ref.Parts = []string{nameOf(sig[j])}
				j++
				for j+1 < n && isPunct(sig[j], ".") && isName(sig[j+1]) {
					ref.Parts = append(ref.Parts, nameOf(sig[j+1]))
					j += 2
				}
			default:
				j = n + 1 // not a table list
			}
			if j > n {
				break
			}
			// Optional alias.
			if j < n && isKeyword(sig[j], "AS") {
				j++
			}
			if j < n && isName(sig[j]) && !(sig[j].Kind == TokIdent && aliasStops[strings.ToUpper(sig[j].Text)]) {
				ref.Alias = nameOf(sig[j])
				j++
			}
			if ref.Parts != nil || ref.Alias != "" {
				out = append(out, ref)
			}
			if list && j < n && isPunct(sig[j], ",") {
				j++
				continue
			}
			break
		}
	}
	return out
}

// skipParens returns the index after the parenthesis group starting at i.
func skipParens(sig []Token, i int) int {
	depth := 0
	for ; i < len(sig); i++ {
		switch {
		case isPunct(sig[i], "("):
			depth++
		case isPunct(sig[i], ")"):
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return i
}

func isPunct(t Token, s string) bool { return t.Kind == TokPunct && t.Text == s }

func isKeyword(t Token, words ...string) bool {
	if t.Kind != TokIdent {
		return false
	}
	for _, w := range words {
		if strings.EqualFold(t.Text, w) {
			return true
		}
	}
	return false
}

func isName(t Token) bool {
	return (t.Kind == TokIdent && !strings.HasPrefix(t.Text, "@")) || t.Kind == TokQuotedIdent
}

func nameOf(t Token) string {
	if t.Kind == TokQuotedIdent {
		return unquoteIdent(t.Text)
	}
	return t.Text
}

// unquoteIdent removes the quotes of a quoted identifier (`…`, "…", […])
// and undoubles the closing quote. A missing closing quote is fine (the
// identifier is still being typed).
func unquoteIdent(s string) string {
	if s == "" {
		return s
	}
	closeq := s[0]
	switch closeq {
	case '[':
		closeq = ']'
	case '`', '"':
	default:
		return s
	}
	var b strings.Builder
	body := s[1:]
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == closeq {
			if i+1 < len(body) && body[i+1] == closeq {
				b.WriteByte(c)
				i++
				continue
			}
			break
		}
		b.WriteByte(c)
	}
	return b.String()
}

func closingQuote(open byte) byte {
	if open == '[' {
		return ']'
	}
	return open
}

// closedQuoted reports whether a quoted identifier token ends with its
// closing quote.
func closedQuoted(s string) bool {
	if len(s) < 2 {
		return false
	}
	q := closingQuote(s[0])
	for i := 1; i < len(s); i++ {
		if s[i] == q {
			if i+1 < len(s) && s[i+1] == q {
				i++
				continue
			}
			return i == len(s)-1
		}
	}
	return false
}

// unterminated reports whether a string or comment token runs to the end
// of the input without its closing sequence (the cursor at its end is
// still inside).
func unterminated(t Token) bool {
	s := t.Text
	switch t.Kind {
	case TokComment, TokExecComment:
		if strings.HasPrefix(s, "/*") {
			return len(s) < 4 || !strings.HasSuffix(s, "*/")
		}
		// Line comment: closed only by the line break.
		return !strings.HasSuffix(s, "\n")
	case TokString:
		// Dollar quoting: $tag$…$tag$.
		if strings.HasPrefix(s, "$") {
			if k := strings.Index(s[1:], "$"); k >= 0 {
				tag := s[:k+2]
				return len(s) < 2*len(tag) || !strings.HasSuffix(s, tag)
			}
			return true
		}
		// Prefixes like E'…', N'…', X'…', B'…', U&'…'.
		i := strings.IndexAny(s, `'"`)
		if i < 0 {
			return false
		}
		q := s[i]
		body := s[i+1:]
		if !strings.HasSuffix(body, string(q)) {
			return true
		}
		// Count trailing quotes: an odd number closes (doubled quotes
		// escape), unless the last one is backslash-escaped.
		k := 0
		for k < len(body) && body[len(body)-1-k] == q {
			k++
		}
		if k == len(body) { // body is only quotes, e.g. '' or ''''
			return k%2 == 0
		}
		if body[len(body)-1-k] == '\\' {
			// Backslash before the quote run: count backslashes.
			b := 0
			for b < len(body)-k && body[len(body)-1-k-b] == '\\' {
				b++
			}
			if b%2 == 1 {
				k-- // one quote is escaped
			}
		}
		return k%2 == 0
	}
	return false
}
