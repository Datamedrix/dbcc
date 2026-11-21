package editor

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/gdamore/tcell/v2"
)

// Styles are the colours of the editor. Token styles with the default
// background are drawn on the box background.
type Styles struct {
	Text, Keyword, Builtin, Name, String, Number, Comment, Operator tcell.Style

	Gutter        tcell.Style // line numbers
	GutterCurrent tcell.Style // line number of the cursor line
	Marker        tcell.Style // gutter marker of the current statement
	MarkerRune    rune        // default '▎'
	Selection     tcell.Style
	// StatementBg is the background of the current statement's text.
	// tcell.ColorDefault (the default) disables it; the gutter marker is
	// always drawn.
	StatementBg tcell.Color
}

// DefaultStyles returns styles for a terminal with the given number of
// colours. Only the 16 basic colours are used, so they degrade cleanly.
func DefaultStyles(colors int) Styles {
	d := tcell.StyleDefault
	s := Styles{
		Text:          d,
		Keyword:       d.Foreground(tcell.ColorBlue).Bold(true),
		Builtin:       d.Foreground(tcell.ColorTeal),
		Name:          d,
		String:        d.Foreground(tcell.ColorGreen),
		Number:        d.Foreground(tcell.ColorPurple),
		Comment:       d.Foreground(tcell.ColorGray).Italic(true),
		Operator:      d.Foreground(tcell.ColorOlive),
		Gutter:        d.Foreground(tcell.ColorGray),
		GutterCurrent: d.Bold(true),
		Marker:        d.Foreground(tcell.ColorTeal),
		MarkerRune:    '▎',
		Selection:     d.Reverse(true),
		StatementBg:   tcell.ColorDefault,
	}
	if colors < 16 && colors > 0 {
		s.Builtin, s.String, s.Number, s.Operator = d, d.Underline(true), d, d
		s.Keyword = d.Bold(true)
		s.Comment = d.Dim(true)
		s.Gutter, s.Marker = d.Dim(true), d.Bold(true)
	}
	return s
}

// tokenClass maps chroma token types to the style classes.
func (s *Styles) forToken(t chroma.TokenType) tcell.Style {
	switch {
	case t.InCategory(chroma.Comment):
		return s.Comment
	case t.InCategory(chroma.LiteralString):
		return s.String
	case t.InCategory(chroma.LiteralNumber):
		return s.Number
	case t.InCategory(chroma.Keyword):
		return s.Keyword
	case t == chroma.NameBuiltin || t == chroma.NameBuiltinPseudo || t.InSubCategory(chroma.NameFunction):
		return s.Builtin
	case t.InCategory(chroma.Name):
		return s.Name
	case t.InCategory(chroma.Operator) || t.InCategory(chroma.Punctuation):
		return s.Operator
	}
	return s.Text
}

// SetLexer selects the chroma lexer by name: "mysql", "postgresql",
// "plpgsql", "tsql"; anything else (or an unknown name) falls back to "sql".
func (e *Editor) SetLexer(name string) {
	var l chroma.Lexer
	switch strings.ToLower(name) {
	case "mysql", "mariadb":
		l = lexers.Get("mysql")
	case "postgresql", "postgres", "pgsql":
		l = lexers.Get("postgresql")
	case "plpgsql":
		l = lexers.Get("plpgsql")
	case "tsql", "t-sql", "mssql":
		l = lexers.Get("tsql")
	}
	if l == nil {
		l = lexers.Get("sql")
	}
	e.lexerName = name
	e.lexer = l
	e.hlVer = -1
}

// SetStyles replaces the editor styles.
func (e *Editor) SetStyles(s Styles) {
	if s.MarkerRune == 0 {
		s.MarkerRune = '▎'
	}
	e.styles = s
	e.hlVer = -1
}

// GetStyles returns the current styles.
func (e *Editor) GetStyles() Styles { return e.styles }

// highlight returns per-byte styles for each line, cached per version.
func (e *Editor) highlight() [][]tcell.Style {
	if e.hlVer == e.version && e.hl != nil {
		return e.hl
	}
	res := make([][]tcell.Style, len(e.lines))
	for i, l := range e.lines {
		st := make([]tcell.Style, len(l))
		for j := range st {
			st[j] = e.styles.Text
		}
		res[i] = st
	}
	if e.lexer != nil {
		if it, err := e.lexer.Tokenise(nil, e.Text()); err == nil {
			row, col := 0, 0
		tokens:
			for t := it(); t != chroma.EOF; t = it() {
				st := e.styles.forToken(t.Type)
				for i := 0; i < len(t.Value); i++ {
					if row >= len(res) {
						break tokens
					}
					if t.Value[i] == '\n' {
						row, col = row+1, 0
						continue
					}
					if col < len(res[row]) {
						res[row][col] = st
					}
					col++
				}
			}
		}
	}
	e.hl, e.hlVer = res, e.version
	return res
}

// StyleAt returns the highlight style of the byte at 1-based line and byte
// column (for tests and the cat view).
func (e *Editor) StyleAt(line, col int) tcell.Style {
	hl := e.highlight()
	if line < 1 || line > len(hl) || col < 0 || col >= len(hl[line-1]) {
		return e.styles.Text
	}
	return hl[line-1][col]
}
