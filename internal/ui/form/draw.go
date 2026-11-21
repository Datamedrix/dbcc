package form

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

// put prints s at (x, y) without interpreting tags, clipped to maxW cells,
// and returns the width printed.
func put(screen tcell.Screen, x, y, maxW int, s string, st tcell.Style) int {
	if maxW <= 0 {
		return 0
	}
	w := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		rs := g.Runes()
		cw := g.Width()
		if cw == 0 {
			continue
		}
		if w+cw > maxW {
			break
		}
		screen.SetContent(x+w, y, rs[0], rs[1:], st)
		w += cw
	}
	return w
}

// clip shortens s to at most n cells, ending in "…" when cut.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if uniseg.StringWidth(s) <= n {
		return s
	}
	var b strings.Builder
	w := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		cw := g.Width()
		if w+cw > n-1 {
			break
		}
		b.WriteString(g.Str())
		w += cw
	}
	return b.String() + "…"
}

// oneLine renders a value as a single preview line.
func oneLine(s string) string {
	multi := false
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s, multi = s[:i], true
	}
	s = strings.ReplaceAll(s, "\t", " ")
	if multi {
		s += " …"
	}
	return s
}

// fill clears a rectangle with style st.
func fill(screen tcell.Screen, x, y, w, h int, st tcell.Style) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			screen.SetContent(x+i, y+j, ' ', nil, st)
		}
	}
}

func width(s string) int { return uniseg.StringWidth(s) }
