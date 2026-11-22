package ui

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/gdamore/tcell/v2"
)

// filterTty sits between the terminal and tcell's input parser and
// rewrites key presses that tcell would misread as the start of a control
// string.
//
// Legacy terminals send Alt+<key> as ESC <key>. For a few keys that is
// also an ECMA-48 introducer: ESC X (SOS), ESC ^ (PM), ESC _ (APC) and
// ESC ] (OSC) start a string that tcell swallows until ESC \ or BEL —
// Alt+Shift+X made every following key (Ctrl+Q, F10 …) vanish and dbcc
// looked frozen. ESC N (SS2) and ESC O (SS3) swallow the next key. Such a
// key press is rewritten to its CSI-u form (ESC [ <code> ; 3 u = Alt+key),
// which tcell reports as an ordinary Alt+rune event.
type filterTty struct {
	tcell.Tty
	mu      sync.Mutex
	pending []byte
}

func newFilterTty(t tcell.Tty) *filterTty { return &filterTty{Tty: t} }

func (f *filterTty) Read(p []byte) (int, error) {
	f.mu.Lock()
	if len(f.pending) > 0 {
		n := copy(p, f.pending)
		f.pending = f.pending[n:]
		f.mu.Unlock()
		return n, nil
	}
	f.mu.Unlock()
	buf := make([]byte, len(p))
	n, err := f.Tty.Read(buf)
	if n <= 0 {
		return n, err
	}
	out := fixEscapes(buf[:n])
	m := copy(p, out)
	if m < len(out) {
		f.mu.Lock()
		f.pending = append(f.pending, out[m:]...)
		f.mu.Unlock()
	}
	return m, err
}

// fixEscapes rewrites ESC <c> key presses that tcell would misparse
// (see filterTty). One read carries one key press, or a few when typed
// fast; real control strings from the terminal are kept:
//   - ESC X, ESC ^, ESC _ (dbcc never requests SOS/PM/APC): rewritten
//     unless a string terminator (ESC \ or BEL) follows in the same read;
//   - ESC ], ESC N, ESC O (OSC replies, SS3 cursor keys exist): rewritten
//     only at the end of the read or before the next ESC.
func fixEscapes(b []byte) []byte {
	if bytes.IndexByte(b, 0x1b) < 0 {
		return b
	}
	var out []byte
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != 0x1b || i+1 >= len(b) {
			out = append(out, c)
			continue
		}
		n := b[i+1]
		rewrite := false
		switch n {
		case 'X', '^', '_':
			rest := b[i+2:]
			rewrite = !bytes.Contains(rest, []byte("\x1b\\")) && bytes.IndexByte(rest, 0x07) < 0
		case ']', 'N', 'O':
			rewrite = i+2 >= len(b) || b[i+2] == 0x1b
		}
		if !rewrite {
			out = append(out, c)
			continue
		}
		out = fmt.Appendf(out, "\x1b[%d;3u", n)
		i++
	}
	return out
}
