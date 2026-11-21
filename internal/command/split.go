package command

import (
	"errors"
	"strings"
	"unicode"
)

// ErrUnterminated is returned by Split for an open quote.
var ErrUnterminated = errors.New("unterminated quote")

// Split splits a command line into words with shell-like quoting:
// '…' is literal, "…" allows \" and \\ escapes, a backslash outside quotes
// escapes the next character. Adjacent quoted and unquoted parts join
// into one word; an empty quoted string is an empty word.
func Split(line string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			inWord = true
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				cur.WriteRune(rs[j])
				j++
			}
			if j >= len(rs) {
				return nil, ErrUnterminated
			}
			i = j
		case c == '"':
			inWord = true
			j := i + 1
			for ; j < len(rs) && rs[j] != '"'; j++ {
				if rs[j] == '\\' && j+1 < len(rs) && (rs[j+1] == '"' || rs[j+1] == '\\') {
					j++
				}
				cur.WriteRune(rs[j])
			}
			if j >= len(rs) {
				return nil, ErrUnterminated
			}
			i = j
		case c == '\\':
			inWord = true
			if i+1 < len(rs) {
				i++
				cur.WriteRune(rs[i])
			}
		default:
			inWord = true
			cur.WriteRune(c)
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// FirstWord returns the first whitespace-delimited word of line as typed
// (no unquoting: a quoted first word is never a command name).
func FirstWord(line string) string {
	f := strings.Fields(line)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}
