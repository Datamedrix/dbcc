// Package command is the command registry and dispatch (spec 6, 8). It
// holds no command logic: every command is a Lua script run by
// internal/script. The registry loads the embedded defaults and the user
// scripts, splits command lines, checks arity and dispatches either to a
// command or, when the first word is not a command, to the SQL runner.
//
// Keys, tree actions and the footer invoke commands by name through
// Dispatch or Run, so a replaced command changes the UI as well.
package command

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dbcc/internal/exec"
	"dbcc/internal/script"
	"dbcc/internal/script/commands"
)

// Registry wraps a script runtime with loading order and dispatch. Like
// the runtime it must be used from one goroutine.
type Registry struct {
	rt *script.Runtime
}

// New returns a registry on rt without any commands loaded.
func New(rt *script.Runtime) *Registry { return &Registry{rt: rt} }

// Runtime returns the underlying Lua runtime.
func (r *Registry) Runtime() *script.Runtime { return r.rt }

// Options select what Load loads.
type Options struct {
	// NoUserCommands loads the embedded defaults only (--no-user-commands).
	NoUserCommands bool
	// UserDir is the user command directory; "" means
	// config.ConfigDir()/commands (the caller passes it in).
	UserDir string
}

// Load loads the embedded defaults, then (unless disabled) the user
// scripts of opts.UserDir. A default that fails to load is a bug and
// returns an error. A user script that fails is reported through
// Host.Message and skipped; a default of the same name stays active.
func (r *Registry) Load(opts Options) error {
	if err := r.LoadDefaults(); err != nil {
		return err
	}
	if !opts.NoUserCommands && opts.UserDir != "" {
		r.LoadUserDir(opts.UserDir)
	}
	return nil
}

// LoadDefaults loads the embedded default scripts in name order.
func (r *Registry) LoadDefaults() error {
	names, err := DefaultScripts()
	if err != nil {
		return err
	}
	for _, n := range names {
		b, err := commands.FS.ReadFile(n)
		if err != nil {
			return err
		}
		if _, err := r.rt.Load("embedded:"+n, string(b), true); err != nil {
			return fmt.Errorf("default command script: %w", err)
		}
	}
	return nil
}

// DefaultScripts lists the embedded script file names, sorted.
func DefaultScripts() ([]string, error) {
	names, err := fs.Glob(commands.FS, "*.lua")
	sort.Strings(names)
	return names, err
}

// LoadUserDir loads dir/*.lua in name order. A missing directory is fine.
// It returns the number of scripts that loaded.
func (r *Registry) LoadUserDir(dir string) int {
	files, err := filepath.Glob(filepath.Join(dir, "*.lua"))
	if err != nil {
		r.report(fmt.Errorf("user commands in %s: %w", dir, err))
		return 0
	}
	sort.Strings(files)
	ok := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			r.report(fmt.Errorf("user command script skipped: %w", err))
			continue
		}
		if _, err := r.rt.Load(f, string(b), false); err != nil {
			r.report(fmt.Errorf("user command script skipped: %w", err))
			continue
		}
		ok++
	}
	return ok
}

func (r *Registry) report(err error) {
	r.rt.Env().Host.Message(exec.Message{Time: time.Now(), Err: err, Info: err.Error(), Affected: -1, Returned: -1})
}

// Lookup returns the command registered under name.
func (r *Registry) Lookup(name string) (*script.Command, bool) { return r.rt.Command(name) }

// Commands lists the registered commands sorted by name (help, completion).
func (r *Registry) Commands() []*script.Command { return r.rt.Commands() }

// Complete returns the command names starting with prefix, sorted.
func (r *Registry) Complete(prefix string) []string {
	var out []string
	for _, c := range r.rt.Commands() {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c.Name)
		}
	}
	return out
}

// IsCommand reports whether the first word of line is a registered command.
func (r *Registry) IsCommand(line string) bool {
	_, ok := r.rt.Command(FirstWord(strings.TrimSuffix(strings.TrimSpace(line), ";")))
	return ok
}

// ErrNoSession is returned when SQL is entered at "/".
var ErrNoSession = errors.New("not connected: cd /<profile> first")

// Dispatch runs one command line: if the first word is a registered
// command, the line is split into words, the arity is checked and the
// command runs; otherwise the whole line is SQL for the runner of the
// current session (Runner.RunScript). Errors: *script.UsageError for a bad
// invocation, *script.CommandError for a failing command, the runner's
// errors (*exec.StmtError) for SQL.
func (r *Registry) Dispatch(ctx context.Context, line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	// A trailing ";" is tolerated after a command ("pwd;").
	cmdLine := strings.TrimSpace(strings.TrimSuffix(line, ";"))
	cmd, ok := r.rt.Command(FirstWord(cmdLine))
	if !ok {
		return r.runSQL(ctx, line)
	}
	words, err := Split(cmdLine)
	if err != nil {
		return &script.UsageError{Command: cmd.Name, Msg: err.Error()}
	}
	return r.Run(ctx, cmd.Name, words[1:]...)
}

// Run invokes a command by name with already split arguments (tree
// actions and keys use this with absolute object paths).
func (r *Registry) Run(ctx context.Context, name string, args ...string) error {
	cmd, ok := r.rt.Command(name)
	if !ok {
		return &script.UsageError{Msg: fmt.Sprintf("unknown command %q", name)}
	}
	if err := cmd.CheckArgs(args); err != nil {
		return err
	}
	return r.rt.Run(ctx, cmd, args)
}

func (r *Registry) runSQL(ctx context.Context, line string) error {
	env := r.rt.Env()
	s := env.Workspace.Current()
	if s == nil {
		return ErrNoSession
	}
	return env.Runner(s).RunScript(ctx, line, 0)
}
