package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2/terminfo"
)

func TestEmbeddedTerminals(t *testing.T) {
	t.Setenv("COLORTERM", "")
	for _, name := range []string{"xterm-256color", "screen-256color", "tmux-256color", "linux", "xterm", "screen"} {
		ti, err := terminfo.LookupTerminfo(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if name != "linux" && name != "xterm" && name != "screen" && ti.Colors < 256 {
			t.Errorf("%s: %d colours, want 256", name, ti.Colors)
		}
	}
}
