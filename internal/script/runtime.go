package script

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	lua "github.com/yuin/gopher-lua"

	"dbcc/internal/exec"
	"dbcc/internal/history"
	"dbcc/internal/session"
)

// Env is what the runtime needs from the application.
type Env struct {
	// Workspace holds the open profiles and the current path. Required.
	Workspace *session.Workspace
	// Host receives output and interaction. Required.
	Host Host
	// Runner returns the runner of a session (one per session, created by
	// the application with its settings, sink and confirm hook). Required
	// for primitives that execute SQL.
	Runner func(s *session.Session) *exec.Runner
	// History is the statement history for ctx:history; nil means none.
	History *history.History
	// Reload, if set, schedules reloading all commands (embedded defaults
	// and user scripts) after the running command has finished; it
	// returns where user scripts are read from. Nil: not available
	// (headless mode runs a single command).
	Reload func() (userDir string, err error)
}

// LoadTimeout bounds the time a script may run while it loads, so a
// script with an endless loop cannot block start-up.
var LoadTimeout = 5 * time.Second

// Runtime is the Lua runtime with the command API (spec 13).
//
// Concurrency: a Runtime owns ONE Lua state and must be used from ONE
// goroutine only (the UI calls it from a single worker goroutine; headless
// mode calls it from main). None of its methods are safe for concurrent
// use. Primitives block until their database work is done.
type Runtime struct {
	L   *lua.LState
	env Env

	commands map[string]*Command
	// cmdMu guards commands: the registry is read from the UI goroutine
	// (help, completion) while the worker loads scripts.
	cmdMu sync.RWMutex
	// staged collects registrations while a script loads; they are
	// committed only when the whole script loaded without error.
	staged   []*Command
	loading  bool
	loadSrc  string
	loadDef  bool
	inv      *invocation
	ctxMeta  *lua.LTable
	pathMeta *lua.LTable
	objMeta  *lua.LTable
	// goErr remembers the Go error behind the last raised Lua error so
	// that callers can still use errors.Is/As (keyed by its message).
	goErr    error
	goErrMsg string
}

type invocation struct {
	cmd *Command
	ctx context.Context
	// confirmed: the owner said yes to ctx:confirm in this write command;
	// its following ctx:exec calls do not ask again.
	confirmed bool
}

// New creates a runtime with the sandboxed Lua state and the API.
func New(env Env) *Runtime {
	if env.Workspace == nil || env.Host == nil {
		panic("script.New: Workspace and Host are required")
	}
	rt := &Runtime{env: env, commands: map[string]*Command{}}
	rt.L = newSandbox()
	rt.installPrint()
	rt.installAPI()
	return rt
}

// Close releases the Lua state.
func (rt *Runtime) Close() { rt.L.Close() }

// Env returns the environment the runtime was created with.
func (rt *Runtime) Env() Env { return rt.env }

// Command returns the registered command with the given name.
func (rt *Runtime) Command(name string) (*Command, bool) {
	rt.cmdMu.RLock()
	defer rt.cmdMu.RUnlock()
	c, ok := rt.commands[name]
	return c, ok
}

// Commands returns all registered commands sorted by name.
func (rt *Runtime) Commands() []*Command {
	rt.cmdMu.RLock()
	out := make([]*Command, 0, len(rt.commands))
	for _, c := range rt.commands {
		out = append(out, c)
	}
	rt.cmdMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Load runs a command script. chunk names it in error messages; isDefault
// marks the embedded defaults. All registrations of the script take effect
// only if it loads completely; on any error none do (so a broken user
// override leaves the default active). It returns the registered names.
func (rt *Runtime) Load(chunk, src string, isDefault bool) ([]string, error) {
	if rt.inv != nil || rt.loading {
		return nil, errors.New("cannot load a script while a command runs")
	}
	fn, err := rt.L.Load(strings.NewReader(src), chunk)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", chunk, cleanLuaErr(err))
	}
	// Each script gets its own global environment; reads fall through to
	// the shared globals, writes stay local to the script.
	envT := rt.L.NewTable()
	mt := rt.L.NewTable()
	mt.RawSetString("__index", rt.L.Get(lua.GlobalsIndex))
	rt.L.SetMetatable(envT, mt)
	fn.Env = envT

	ctx, cancel := context.WithTimeout(context.Background(), LoadTimeout)
	defer cancel()
	rt.L.SetContext(ctx)
	defer rt.L.RemoveContext()

	rt.loading, rt.loadSrc, rt.loadDef, rt.staged = true, chunk, isDefault, nil
	defer func() { rt.loading, rt.staged = false, nil }()
	rt.L.Push(fn)
	if err := rt.L.PCall(0, 0, nil); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: loading took longer than %s", chunk, LoadTimeout)
		}
		return nil, fmt.Errorf("%s: %v", chunk, cleanLuaErr(err))
	}
	names := make([]string, 0, len(rt.staged))
	rt.cmdMu.Lock()
	defer rt.cmdMu.Unlock()
	for _, c := range rt.staged {
		if old, ok := rt.commands[c.Name]; ok && !c.Default && (old.Default || old.Replaces) {
			c.Replaces = true
		}
		rt.commands[c.Name] = c
		names = append(names, c.Name)
	}
	return names, nil
}

// LoadReader is Load for a reader.
func (rt *Runtime) LoadReader(chunk string, r io.Reader, isDefault bool) ([]string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return rt.Load(chunk, string(b), isDefault)
}

// Run executes a command with its (already split) arguments. The caller
// checks the arity first (Command.CheckArgs); Run checks it again.
// Write commands are blocked in read-only profiles. Errors carry the
// command name (*CommandError, or *UsageError for a bad invocation).
func (rt *Runtime) Run(ctx context.Context, cmd *Command, args []string) error {
	if rt.inv != nil {
		return &CommandError{Command: cmd.Name, Msg: "a command is already running"}
	}
	if err := cmd.CheckArgs(args); err != nil {
		return err
	}
	flags, args, _ := cmd.ParseFlags(args)
	if cmd.Write {
		if s := rt.env.Workspace.Current(); s != nil && s.Profile.ReadOnly {
			return &CommandError{Command: cmd.Name, Msg: fmt.Sprintf("writes and is blocked in the read-only profile %s", s.Profile.Name), Err: exec.ErrReadOnly}
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rt.inv = &invocation{cmd: cmd, ctx: ctx}
	rt.goErr, rt.goErrMsg = nil, ""
	defer func() { rt.inv = nil }()
	rt.L.SetContext(ctx)
	defer rt.L.RemoveContext()

	L := rt.L
	argT := L.CreateTable(len(args), 0)
	for _, a := range args {
		argT.Append(lua.LString(a))
	}
	flagT := L.NewTable()
	for k, v := range flags {
		switch x := v.(type) {
		case bool:
			flagT.RawSetString(k, lua.LBool(x))
		case string:
			flagT.RawSetString(k, lua.LString(x))
		}
	}
	L.Push(cmd.fn)
	L.Push(rt.newCtx(""))
	L.Push(argT)
	L.Push(flagT)
	if err := L.PCall(3, 0, nil); err != nil {
		ce := &CommandError{Command: cmd.Name, Msg: cleanLuaErr(err)}
		var ae *lua.ApiError
		if errors.As(err, &ae) {
			ce.Trace = ae.StackTrace
		}
		if ctx.Err() != nil {
			ce.Err = ctx.Err()
		} else if rt.goErr != nil && strings.Contains(ce.Msg, rt.goErrMsg) {
			ce.Err = rt.goErr
		}
		return ce
	}
	return nil
}

// cleanLuaErr returns the error value of a Lua error without stack trace.
func cleanLuaErr(err error) string {
	var ae *lua.ApiError
	if errors.As(err, &ae) {
		if ae.Object != nil {
			return ae.Object.String()
		}
		if ae.Cause != nil {
			return ae.Cause.Error()
		}
	}
	return err.Error()
}

// raise turns a Go error into a Lua error without position information
// and remembers the Go error.
func (rt *Runtime) raise(err error) int {
	msg := err.Error()
	rt.goErr, rt.goErrMsg = err, msg
	rt.L.Error(lua.LString(msg), 0)
	return 0
}

func (rt *Runtime) raisef(format string, a ...any) int {
	return rt.raise(fmt.Errorf(format, a...))
}

// installPrint routes print() to the message log.
func (rt *Runtime) installPrint() {
	rt.L.SetGlobal("print", rt.L.NewFunction(func(L *lua.LState) int {
		n := L.GetTop()
		parts := make([]string, n)
		for i := 1; i <= n; i++ {
			parts[i-1] = L.ToStringMeta(L.Get(i)).String()
		}
		rt.message(strings.Join(parts, "\t"))
		return 0
	}))
}

func (rt *Runtime) message(text string) {
	rt.env.Host.Message(exec.Message{Time: time.Now(), Info: text, Affected: -1, Returned: -1})
}
