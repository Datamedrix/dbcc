package ui

import (
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/rivo/tview"
)

// tokenColor maps chroma token categories to tview colour names that exist
// in the 16-colour palette, so highlighting degrades gracefully.
func tokenColor(t chroma.TokenType) string {
	switch {
	case t.InCategory(chroma.Comment):
		return "gray"
	case t.InCategory(chroma.Keyword):
		return "aqua"
	case t.InSubCategory(chroma.LiteralString):
		return "green"
	case t.InSubCategory(chroma.LiteralNumber):
		return "fuchsia"
	case t == chroma.NameBuiltin || t == chroma.NameFunction:
		return "yellow"
	case t.InCategory(chroma.Operator):
		return "silver"
	}
	return ""
}

// HighlightTags renders text with tview colour tags using a chroma lexer.
// Unknown lexers and "" return the escaped text unchanged.
func HighlightTags(text, lexer string) string {
	if lexer == "" {
		return tview.Escape(text)
	}
	l := lexers.Get(lexer)
	if l == nil {
		return tview.Escape(text)
	}
	it, err := chroma.Coalesce(l).Tokenise(nil, text)
	if err != nil {
		return tview.Escape(text)
	}
	var b strings.Builder
	for _, tok := range it.Tokens() {
		c := tokenColor(tok.Type)
		v := tview.Escape(tok.Value)
		if c == "" {
			b.WriteString(v)
			continue
		}
		// Colour per line so tags never span a newline.
		lines := strings.Split(v, "\n")
		for i, ln := range lines {
			if i > 0 {
				b.WriteByte('\n')
			}
			if ln != "" {
				fmt.Fprintf(&b, "[%s]%s[-]", c, ln)
			}
		}
	}
	return b.String()
}
