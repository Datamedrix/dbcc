package grid

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rivo/uniseg"

	"dbcc/internal/exec"
)

const (
	ellipsis    = "…"
	newlineMark = "↵"
)

// rawText returns the text shown for a cell value and whether it is NULL.
func rawText(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "NULL", true
	case exec.Binary:
		return fmt.Sprintf("<BLOB %d bytes>", len(x)), false
	case string:
		return x, false
	default:
		return exec.CellText(v), false
	}
}

// fit renders s for one cell line of at most max display columns: newlines
// become "↵", tabs a space, other control characters "?". If s is wider than
// max it is cut and ends in "…". It returns the text and its display width.
// The work is bounded by max, not by len(s), so huge values are cheap.
func fit(s string, max int) (string, int) {
	if max <= 0 {
		return "", 0
	}
	var b strings.Builder
	width := 0
	cutLen, cutWidth := 0, 0 // longest prefix that leaves room for "…"
	state := -1
	for len(s) > 0 {
		var cluster string
		var w int
		cluster, s, w, state = uniseg.FirstGraphemeClusterInString(s, state)
		switch {
		case cluster == "\n" || cluster == "\r\n" || cluster == "\r":
			cluster, w = newlineMark, 1
		case cluster == "\t":
			cluster, w = " ", 1
		case len(cluster) == 1 && (cluster[0] < 0x20 || cluster[0] == 0x7f):
			cluster, w = "?", 1
		}
		if width+w > max {
			out := b.String()[:cutLen] + ellipsis
			return out, cutWidth + 1
		}
		b.WriteString(cluster)
		width += w
		if width <= max-1 {
			cutLen, cutWidth = b.Len(), width
		}
	}
	return b.String(), width
}

// textWidth is the display width of v in a cell, capped at max+1 (so the
// caller can tell "fits" from "needs cutting" without scanning more).
func textWidth(s string, max int) int {
	_, w := fit(s, max+1)
	return w
}

// CellDetailText returns the full text of a cell value for the detail
// window (spec 10.1): NULL as "NULL", binary values as a size line plus a hex
// dump (at most 64 KiB), JSON objects and arrays pretty-printed, anything
// else unchanged.
func CellDetailText(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case exec.Binary:
		const limit = 64 << 10
		head := fmt.Sprintf("<BLOB %d bytes>\n\n", len(x))
		if len(x) > limit {
			return head + hex.Dump(x[:limit]) + "…\n"
		}
		return head + hex.Dump(x)
	case string:
		t := strings.TrimSpace(x)
		if len(t) > 1 && (t[0] == '{' || t[0] == '[') && json.Valid([]byte(t)) {
			var buf bytes.Buffer
			if json.Indent(&buf, []byte(t), "", "  ") == nil {
				return buf.String()
			}
		}
		return x
	default:
		return exec.CellText(v)
	}
}
