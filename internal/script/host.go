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
	// Form shows a settings dialog for fields (ctx:form, export settings),
	// pre-filled with values (name -> string, int64 or bool; missing names
	// use the field's default). It returns name -> typed value, or ok =
	// false when the owner cancelled. Headless: an ErrNoUI error.
	Form(title string, fields []OptionSpec, values map[string]any) (map[string]any, bool, error)
	// SaveFile shows the file dialog (ctx:save_file) and returns the chosen
	// path, or ok = false when the owner cancelled. It does not ask about
	// overwriting. Headless: an ErrNoUI error.
	SaveFile(opts SaveFileOptions) (path string, ok bool, err error)
	// OpenFile shows the file dialog in open mode (ctx:open_file, A29):
	// only an existing file is returned (opts.Name is not used). Headless:
	// an ErrNoUI error.
	OpenFile(opts SaveFileOptions) (path string, ok bool, err error)
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

// Interactive is implemented by hosts that can show dialogs (the TUI).
// ctx.ui is its answer, so a script can tell before asking anything
// whether ctx:pick, ctx:form or ctx:save_file are available; hosts without
// the method are not interactive.
type Interactive interface{ Interactive() bool }

// ObjectRef binds an editor tab to a database object.
type ObjectRef struct {
	Path meta.Path
	Kind meta.ObjectKind
}

// SaveFileOptions configure the file dialog of ctx:save_file.
type SaveFileOptions struct {
	Title string
	// Name is the suggested file name.
	Name string
	// Dir is the start directory; "" lets the host choose (the directory
	// of the last file chosen in this session, else the working directory).
	Dir string
	// Extensions without dot; files with them are listed first-class, and
	// a typed name without extension gets the first one appended.
	Extensions []string
}

// ErrNoUI is returned by interactive primitives in headless mode.
var ErrNoUI = errors.New("not available without UI")
