// Package ui is the tview application: layout, panes, footer and help.
package ui

// Spec 5.2: the binary must start without external files or helper programs
// under TERM=xterm-256color, screen-256color, tmux-256color and linux.
// tcell v2.13 embeds xterm*, tmux*, ansi and vt* through terminfo/base;
// screen* and linux must be registered explicitly.
import (
	_ "github.com/gdamore/tcell/v2/terminfo/base"
	_ "github.com/gdamore/tcell/v2/terminfo/l/linux"
	_ "github.com/gdamore/tcell/v2/terminfo/s/screen"
)
