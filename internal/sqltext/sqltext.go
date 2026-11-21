// Package sqltext contains the hand-written SQL tokenizers, statement
// splitters and completion-context helpers (spec sections 9.2, 9.3).
// Chroma is used for colouring only; everything structural lives here.
package sqltext

// TokenKind classifies a token produced by a Tokenizer.
type TokenKind int

const (
	TokEOF TokenKind = iota
	TokWhitespace
	TokComment     // -- …, # …, /* … */
	TokExecComment // MySQL /*! … */ (passed through, treated as code)
	TokString      // '…', "…" (MySQL), E'…', $tag$…$tag$
	TokQuotedIdent // `…`, "…" (Postgres/MSSQL), […] (MSSQL)
	TokIdent       // bare identifier or keyword
	TokNumber
	TokPunct     // ( ) , . = etc.
	TokDelimiter // the active statement delimiter (e.g. ";" or "$$")
	TokParam     // ?, $1, @name
)

// Token is a lexical unit with byte offsets into the source.
type Token struct {
	Kind  TokenKind
	Text  string
	Start int // byte offset, inclusive
	End   int // byte offset, exclusive
	Line  int // 1-based line of Start
}

// Statement is one executable unit found by a Splitter.
type Statement struct {
	Text  string // statement text without the trailing delimiter, trimmed
	Line  int    // 1-based line number where the statement text starts
	Start int    // byte offset of the first character of Text in the source
	End   int    // byte offset just after the statement (including its delimiter, if any)
	Count int    // repeat count (MSSQL "GO n"); 1 otherwise
}

// Splitter splits a script into statements. The same splitter finds the
// statement at the cursor (spec 9.3).
type Splitter interface {
	// Split returns all non-empty statements of src in order.
	// Client-side directives (MySQL DELIMITER lines, MSSQL GO lines) are
	// consumed and never appear in the output.
	Split(src string) ([]Statement, error)
	// StatementAt returns the statement that contains byte offset pos, or
	// the nearest preceding one if pos is in whitespace between statements.
	StatementAt(src string, pos int) (Statement, bool)
}

// Tokenizer produces tokens for completion context and highlighting helpers.
type Tokenizer interface {
	Tokenize(src string) []Token
}
