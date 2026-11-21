// Package script is the Lua runtime and its API primitives (spec 13).
// Go provides primitives; every command is a Lua script.
package script

import (
	"errors"

	"dbcc/internal/exec"
	"dbcc/internal/meta"
)

// Host is where command output and interaction go. Headless mode prints to
// stdout/stderr; the TUI opens tabs and dialogs. Every primitive works in
// both (spec 13, last paragraph).
type Host interface {
	// Result shows a result set (ctx:grid, ctx:rows) in a result tab.
	Result(r *exec.Result)
	// Text shows text in a tab, highlighted with a chroma lexer ("" = plain).
	Text(title, text, lexer string)
	// Message writes to the message log (also receives runner messages).
	Message(m exec.Message)
	// Confirm asks yes or no. Headless: fails unless --yes.
	Confirm(msg string) (bool, error)
	// Pick lets the owner choose from a list; returns the index or -1.
	Pick(title string, items []string) (int, error)
	// EditorSet puts text into the current scratch tab.
	EditorSet(text string) error
	// EditorOpen opens or focuses an editor tab, optionally bound to an object.
	EditorOpen(title, text string, obj *ObjectRef) error
	// About shows the About page (headless: prints it).
	About() error
	// ActiveResult returns the active result (the current result tab; in
	// headless mode the last result) and its local view, or nil.
	ActiveResult() (*exec.Result, exec.View)
}

// ObjectRef binds an editor tab to a database object.
type ObjectRef struct {
	Path meta.Path
	Kind meta.ObjectKind
}

// ErrNoUI is returned by interactive primitives in headless mode.
var ErrNoUI = errors.New("not available without UI")
