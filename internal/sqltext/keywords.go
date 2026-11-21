package sqltext

import "strings"

// Keywords returns the upper-cased bare words of stmt in order, skipping
// whitespace, comments, strings and quoted identifiers. If the first
// significant token is "(", it is returned as "(". n limits the result
// (0 = all). Used for statement classification (read/write, returns rows,
// implicit commit).
func Keywords(t Tokenizer, stmt string, n int) []string {
	var out []string
	first := true
	for _, tok := range t.Tokenize(stmt) {
		switch tok.Kind {
		case TokWhitespace, TokComment:
			continue
		case TokIdent:
			out = append(out, strings.ToUpper(tok.Text))
		case TokPunct:
			if first && tok.Text == "(" {
				out = append(out, "(")
			}
		}
		first = false
		if n > 0 && len(out) >= n {
			break
		}
	}
	return out
}
