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
	// Files is the state of file results that outlives this runtime (A29):
	// the runtime that reads them now, settings remembered per path, page
	// and chunk sizes. Nil: a private state without memory and without
	// paging (headless).
	Files *Files
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
	// exporters are the registered export formats (dbcc.exporter), guarded
	// by cmdMu like commands.
	exporters map[string]*Exporter
	importers map[string]*Importer
	// invMu guards inv for Interrupt (called from other goroutines).
	invMu sync.Mutex
	// cmdMu guards commands: the registry is read from the UI goroutine
	// (help, completion) while the worker loads scripts.
	cmdMu sync.RWMutex
	// staged collects registrations while a script loads; they are
	// committed only when the whole script loaded without error.
	staged    []*Command
	stagedExp []*Exporter
	stagedImp []*Importer
	loading   bool
	loadSrc   string
	loadDef   bool
	inv       *invocation
	ctxMeta   *lua.LTable
	pathMeta  *lua.LTable
	objMeta   *lua.LTable
	fileMeta  *lua.LTable
	// goErr remembers the Go error behind the last raised Lua error so
	// that callers can still use errors.Is/As (keyed by its message).
	goErr    error
	goErrMsg string
	// exportBusy is set while an exporter's write function runs; it stops
	// a nested ctx:export_with from streaming the same result twice.
	exportBusy bool

	// bgCtx/bgStop: the Lua context while an importer runs outside a
	// command (grid paging, rerun); bgOwner is the file source whose Lua
	// code runs now. Guarded by invMu (Interrupt and closing a source come
	// from other goroutines).
	bgCtx   context.Context
	bgStop  context.CancelFunc
	bgOwner *fileSource
	// sources are the open file sources of this runtime; Close closes
	// them first (their Lua functions die with the state).
	srcMu   sync.Mutex
	sources map[*fileSource]struct{}
	closed  bool
}

type invocation struct {
	cmd *Command
	ctx context.Context
	// stop interrupts the Lua code of this invocation (F8, Ctrl+C). It is
	// separate from ctx, which the SQL primitives use: cancelling the SQL
	// context would make the MySQL driver close the pinned connection.
	stop context.CancelFunc
	// confirmed: the owner said yes to ctx:confirm in this write command;
	// its following ctx:exec calls do not ask again.
	confirmed bool
	// chosen holds the paths the owner chose in this invocation (normalised
	// with exportPath): values of "file" arguments and flags, and paths
	// returned by ctx:save_file. ctx:export_with writes only to these.
	chosen map[string]bool
}

// New creates a runtime with the sandboxed Lua state and the API.
func New(env Env) *Runtime {
	if env.Workspace == nil || env.Host == nil {
		panic("script.New: Workspace and Host are required")
	}
	rt := &Runtime{env: env, commands: map[string]*Command{}, exporters: map[string]*Exporter{},
		importers: map[string]*Importer{}, sources: map[*fileSource]struct{}{}}
	if rt.env.Files == nil {
		rt.env.Files = NewFiles(0, 0, false)
		rt.env.Files.Use(rt)
	}
	rt.L = newSandbox()
	rt.installPrint()
	rt.installAPI()
	return rt
}

// Close releases the Lua state. Open file cursors of this runtime are
// closed first and their results marked partial: their Lua functions die
// with the state (reload, A29); paging reopens them through the runtime
// that is current then. Call it on the goroutine that uses the runtime.
func (rt *Runtime) Close() {
	rt.srcMu.Lock()
	if rt.closed {
		rt.srcMu.Unlock()
		return
	}
	rt.closed = true
	srcs := make([]*fileSource, 0, len(rt.sources))
	for s := range rt.sources {
		srcs = append(srcs, s)
	}
	rt.srcMu.Unlock()
	for _, s := range srcs {
		s.closeByRuntime()
	}
	rt.L.Close()
}

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
// override leaves the default active). It returns the registered names
// (export formats as "exporter:<name>", import formats as "importer:<name>").
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

	rt.loading, rt.loadSrc, rt.loadDef, rt.staged, rt.stagedExp, rt.stagedImp = true, chunk, isDefault, nil, nil, nil
	defer func() { rt.loading, rt.staged, rt.stagedExp, rt.stagedImp = false, nil, nil, nil }()
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
	for _, e := range rt.stagedExp {
		if old, ok := rt.exporters[e.Name]; ok && !e.Default && (old.Default || old.Replaces) {
			e.Replaces = true
		}
		rt.exporters[e.Name] = e
		names = append(names, "exporter:"+e.Name)
	}
	for _, e := range rt.stagedImp {
		if old, ok := rt.importers[e.Name]; ok && !e.Default && (old.Default || old.Replaces) {
			e.Replaces = true
		}
		rt.importers[e.Name] = e
		names = append(names, "importer:"+e.Name)
	}
	return names, nil
}

// Exporter returns the registered export format with the given name.
func (rt *Runtime) Exporter(name string) (*Exporter, bool) {
	rt.cmdMu.RLock()
	defer rt.cmdMu.RUnlock()
	e, ok := rt.exporters[name]
	return e, ok
}

// Exporters returns all registered export formats sorted by name.
func (rt *Runtime) Exporters() []*Exporter {
	rt.cmdMu.RLock()
	out := make([]*Exporter, 0, len(rt.exporters))
	for _, e := range rt.exporters {
		out = append(out, e)
	}
	rt.cmdMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// OpenFiles is the number of file sources of this runtime that are open
// (diagnostics and tests: every closed tab must bring it back down).
func (rt *Runtime) OpenFiles() int {
	rt.srcMu.Lock()
	defer rt.srcMu.Unlock()
	return len(rt.sources)
}

// Importer returns the registered import format with the given name.
func (rt *Runtime) Importer(name string) (*Importer, bool) {
	rt.cmdMu.RLock()
	defer rt.cmdMu.RUnlock()
	e, ok := rt.importers[name]
	return e, ok
}

// Importers returns all registered import formats sorted by name.
func (rt *Runtime) Importers() []*Importer {
	rt.cmdMu.RLock()
	out := make([]*Importer, 0, len(rt.importers))
	for _, e := range rt.importers {
		out = append(out, e)
	}
	rt.cmdMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
	luaCtx, stop := context.WithCancel(context.Background())
	defer stop()
	rt.invMu.Lock()
	rt.inv = &invocation{cmd: cmd, ctx: ctx, stop: stop, chosen: chosenFiles(cmd, flags, args)}
	rt.invMu.Unlock()
	rt.goErr, rt.goErrMsg = nil, ""
	defer func() {
		rt.invMu.Lock()
		rt.inv = nil
		rt.invMu.Unlock()
	}()
	rt.L.SetContext(luaCtx)
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
		case []string:
			flagT.RawSetString(k, strList(L, x))
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
		if luaCtx.Err() != nil {
			ce.Msg, ce.Err = "interrupted", context.Canceled
		} else if ctx.Err() != nil {
			ce.Err = ctx.Err()
		} else if rt.goErr != nil && strings.Contains(ce.Msg, rt.goErrMsg) {
			ce.Err = rt.goErr
		}
		return ce
	}
	return nil
}

// chosenFiles collects the normalised values of the positional arguments
// and flags that the command declares with type "file" (and file:<path>
// values of "object" ones): the owner typed them, so the command may write
// (ctx:export_with) and read (ctx:file_grid, A29) there.
func chosenFiles(cmd *Command, flags map[string]any, args []string) map[string]bool {
	out := map[string]bool{}
	for i, v := range args {
		if len(cmd.Args) == 0 {
			break
		}
		spec := cmd.Args[len(cmd.Args)-1]
		if i < len(cmd.Args) {
			spec = cmd.Args[i]
		} else if !spec.Variadic {
			continue
		}
		addChosen(out, spec.Type, v)
	}
	for name, v := range flags {
		f, ok := cmd.Flag(name)
		if !ok {
			continue
		}
		switch x := v.(type) {
		case string:
			addChosen(out, ArgType(f.Type), x)
		case []string:
			for _, p := range x {
				addChosen(out, ArgType(f.Type), p)
			}
		}
	}
	return out
}

// addChosen records the path the owner typed for an argument of type t:
// a "file" value, or an "object" value written file:<path> (A29).
func addChosen(out map[string]bool, t ArgType, v string) {
	switch {
	case t == ArgFile:
		// file:<path> is accepted on file arguments too, as everywhere else.
		out[exportPath(strings.TrimPrefix(v, FilePrefix))] = true
	case t == ArgObject && strings.HasPrefix(v, FilePrefix):
		out[exportPath(strings.TrimPrefix(v, FilePrefix))] = true
	}
}

// Interrupt stops the Lua code of the running command, e.g. an endless
// loop (F8 in the UI, Ctrl+C headless), or of an importer reading a file
// outside a command (grid paging, A29). A primitive that is waiting for the
// database finishes first; cancel the statement through the session for
// that. Safe to call from any goroutine; no-op when nothing runs.
func (rt *Runtime) Interrupt() bool {
	rt.invMu.Lock()
	defer rt.invMu.Unlock()
	if rt.inv == nil || rt.inv.stop == nil {
		// Outside a command an importer may be reading a file for the grid
		// (paging, rerun): stop its Lua code.
		if rt.bgOwner != nil && rt.bgStop != nil {
			rt.bgStop()
			return true
		}
		return false
	}
	rt.inv.stop()
	return true
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
