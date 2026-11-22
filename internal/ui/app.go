package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/command"
	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/history"
	"dbcc/internal/meta"
	"dbcc/internal/script"
	"dbcc/internal/session"
	"dbcc/internal/sqltext"
	"dbcc/internal/ui/complete"
	"dbcc/internal/ui/editor"
	"dbcc/internal/ui/form"
	"dbcc/internal/ui/grid"
	"dbcc/internal/ui/splash"
)

// Options configure the UI.
type Options struct {
	Config         *config.Config
	Profile        string // connect at start (optional)
	NoSplash       bool
	NoUserCommands bool
	Screen         tcell.Screen // tests: simulation screen
	SplashDuration time.Duration
	StateDir       string        // default config.StateDir()
	ConfigPath     string        // where new connections are saved (default config.DefaultPath())
	AutosaveDelay  time.Duration // scratch autosave delay (default 1 s)
	UserCmdDir     string        // default config.ConfigDir()/commands
}

// App is the full-screen terminal UI.
type App struct {
	profileForm *tview.Form // the open connection editor (F2), for tests
	opts        Options
	cfg         *config.Config
	screen      tcell.Screen
	tv          *tview.Application
	pages       *tview.Pages
	theme       *Theme
	keymap      *Keymap
	fkeys       FKeyTranslator

	ws  *session.Workspace
	reg *command.Registry

	tree      *Tree
	treeBox   *tview.Flex
	treeInput *tview.InputField
	editors   *editor.Tabs
	results   *ResultTabs
	log       *MessageLog
	cmd       *tview.InputField
	status    *StatusLine
	footer    *Footer
	right     *tview.Flex

	jobs       chan func()
	busy       atomic.Int32
	splashDone chan struct{}
	quit       chan struct{}
	quitOnce   sync.Once

	mu         sync.Mutex
	runners    map[*session.Session]*exec.Runner
	lastDur    time.Duration
	lastPath   meta.Path
	cmdHistory []string
	histPos    int
	complete   bool // completion list requested by key (spec 5.1)
	cmdList    bool // the command-line completion list is open
	modalStack []string
	lastErr    string

	hist             *history.History
	meta             *meta.Cache
	popup            *complete.Popup
	gridPromptClosed func()
	autosave         *time.Timer // pending scratch autosave
	maximized        string      // PaneEditor or PaneGrid while F11 is active
	whereList        bool        // completion list of the WHERE prompt is open
}

// Run starts the UI and blocks until it quits.
func Run(ctx context.Context, opts Options) error {
	a, err := New(opts)
	if err != nil {
		return err
	}
	return a.Run(ctx)
}

// New builds the application.
func New(opts Options) (*App, error) {
	cfg := opts.Config
	if opts.StateDir == "" {
		opts.StateDir = config.StateDir()
	}
	if opts.UserCmdDir == "" {
		opts.UserCmdDir = filepath.Join(config.ConfigDir(), "commands")
	}
	km := DefaultKeymap()
	if err := km.Apply(cfg.Keys, cfg.Settings.CompletionKeyAlt); err != nil {
		return nil, err
	}
	a := &App{
		opts: opts, cfg: cfg, keymap: km,
		tv:         tview.NewApplication(),
		jobs:       make(chan func(), 64),
		splashDone: make(chan struct{}),
		quit:       make(chan struct{}),
		runners:    map[*session.Session]*exec.Runner{},
	}
	a.ws = session.NewWorkspace(cfg, a.promptPassword)
	a.ws.BeforeDatabaseChange = a.txnDecision
	a.ws.ConfirmHostKey = a.confirmHostKey
	return a, nil
}

// Run builds the layout, shows the splash and runs the event loop.
func (a *App) Run(ctx context.Context) error {
	scr := a.opts.Screen
	if scr == nil {
		var err error
		// The filter keeps Alt+Shift+X & co. from freezing the input
		// (see filterTty); without a /dev/tty fall back to the default.
		if tty, terr := tcell.NewDevTty(); terr == nil {
			scr, err = tcell.NewTerminfoScreenFromTty(newFilterTty(tty))
		}
		if scr == nil {
			if scr, err = tcell.NewScreen(); err != nil {
				return err
			}
		}
	}
	a.screen = scr
	a.tv.SetScreen(scr) // initialises the screen
	colors := scr.Colors()
	a.theme = NewTheme(colors)
	a.build()
	a.tv.EnableMouse(a.cfg.Settings.Mouse)
	a.tv.EnablePaste(true)

	go a.worker()
	a.startWatchdog()

	showSplash := a.cfg.Settings.Splash && !a.opts.NoSplash
	if showSplash {
		sp := splash.New(splash.ModeSplash, func() {
			a.pages.RemovePage("splash")
			a.focusDefault()
			a.closeSplash()
		})
		a.pages.AddPage("splash", sp, true, true)
		a.tv.SetFocus(sp)
		sp.Start(a.queue, a.opts.SplashDuration)
	} else {
		a.closeSplash()
		a.focusDefault()
	}
	// Start-up work runs behind the splash (spec 7.1).
	a.do(a.startup)

	err := a.tv.Run()
	a.shutdown()
	return err
}

func (a *App) closeSplash() {
	select {
	case <-a.splashDone:
	default:
		close(a.splashDone)
	}
}

// startup loads commands, restores scratch tabs and connects (worker).
func (a *App) startup() {
	a.loadCommands(false)
	for _, w := range a.cfg.Warnings {
		a.message(exec.Message{Info: "warning: " + w})
	}
	if a.opts.Profile != "" {
		a.runCommand("cd", "/"+a.opts.Profile)
	}
}

// loadCommands builds a fresh Lua runtime and registry with the embedded
// defaults and the user scripts and publishes it (worker goroutine). On a
// reload the previous runtime is closed afterwards; a reload is scheduled
// as its own job, so no command is running in the old Lua state.
func (a *App) loadCommands(reload bool) {
	rt := script.New(script.Env{Workspace: a.ws, Host: &host{a}, Runner: a.runner, History: a.hist,
		Reload: func() (string, error) {
			a.do(func() { a.loadCommands(true) })
			return a.opts.UserCmdDir, nil
		}})
	reg := command.New(rt)
	if err := reg.Load(command.Options{NoUserCommands: a.opts.NoUserCommands, UserDir: a.opts.UserCmdDir}); err != nil {
		a.message(exec.Message{Err: fmt.Errorf("loading commands: %w", err)})
		if reload {
			rt.Close()
			return // keep the commands that work
		}
	}
	var old *command.Registry
	// Published on the UI goroutine, which reads it (help, completion).
	a.ui(func() { old, a.reg = a.reg, reg })
	if reload {
		if old != nil {
			old.Runtime().Close()
		}
		src := "user scripts from " + a.opts.UserCmdDir
		if a.opts.NoUserCommands {
			src = "built-in commands only: --no-user-commands"
		}
		a.message(exec.Message{Info: fmt.Sprintf("commands reloaded: %d commands (%s)", len(reg.Commands()), src)})
	}
}

func (a *App) shutdown() {
	a.quitOnce.Do(func() { close(a.quit) })
	if a.editors != nil {
		_ = a.editors.SaveSession() // requestQuit already asked if saving failed
	}
	a.ws.CloseAll()
}

// ---------------------------------------------------------------- layout

func (a *App) build() {
	t := a.theme
	a.log = NewMessageLog(t)
	a.results = NewResultTabs(t, a.log)
	a.results.OnChange = func(*ResultTab) { a.updateStatus() }

	a.tree = NewTree(a.ws, t, func(f func()) { go f() }, a.queue)
	a.tree.OnError = func(err error) { a.log.Add(exec.Message{Err: err}) }
	a.tree.SetSelectedFunc(func(*tview.TreeNode) { a.treeAction("open") })
	a.treeInput = tview.NewInputField().SetLabel("/").SetFieldBackgroundColor(tcell.ColorDefault)
	a.treeInput.SetChangedFunc(func(s string) { a.tree.SetFilter(s) })
	a.treeInput.SetDoneFunc(func(k tcell.Key) {
		if k == tcell.KeyEscape {
			a.treeInput.SetText("")
			a.tree.SetFilter("")
		}
		a.treeBox.ResizeItem(a.treeInput, 0, 0)
		a.tv.SetFocus(a.tree)
	})
	a.treeBox = tview.NewFlex().SetDirection(tview.FlexRow).AddItem(a.tree, 0, 1, true).AddItem(a.treeInput, 0, 0, false)
	a.tree.Refresh()

	a.editors = editor.NewTabs()
	a.editors.SetBorder(true).SetTitle(" Editor ").SetTitleAlign(tview.AlignRight)
	a.editors.SetLexer("mysql")
	a.editors.SetSplitter(sqltext.NewMySQL()) // replaced by the dialect's on connect
	a.editors.SetStyles(editor.DefaultStyles(256))
	// Restore scratch tabs synchronously so that the first keystrokes have
	// a tab to go to (local file I/O only).
	// Copied and cut text goes to the system clipboard (OSC 52, works over SSH).
	a.editors.Configure = func(e *editor.Editor) {
		e.OnCopy = func(text string) { a.copyToClipboard(text) }
	}
	a.editors.SetScratchDir(filepath.Join(a.opts.StateDir, "scratches"))
	if err := a.editors.LoadSession(filepath.Join(a.opts.StateDir, "scratch")); err != nil {
		a.log.Add(exec.Message{Err: fmt.Errorf("restore scratch tabs: %w", err)})
	}
	a.ensureScratch()
	a.editors.OnChange = a.scheduleAutosave

	a.results.SetBorder(true)

	a.cmd = tview.NewInputField().SetFieldBackgroundColor(tcell.ColorDefault)
	a.cmd.SetLabel("/ > ")
	a.cmd.SetDoneFunc(a.cmdDone)
	a.cmd.SetAutocompleteFunc(a.cmdComplete)
	a.cmd.SetAutocompletedFunc(a.cmdCompleted)
	a.cmd.SetInputCapture(a.cmdKeys)
	a.cmd.SetBlurFunc(func() { a.cmdList = false }) // tview drops the list on blur

	a.status = NewStatusLine(t)
	a.footer = NewFooter(t)

	a.right = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.editors, 0, editorWeight, false).
		AddItem(a.results, 0, resultsWeight, false)
	body := tview.NewFlex().AddItem(a.treeBox, 32, 0, false).AddItem(a.right, 0, 1, false)
	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, 0, 1, false).
		AddItem(a.cmd, 1, 0, false).
		AddItem(a.status, 1, 0, false).
		AddItem(a.footer, 1, 0, false)

	a.pages = tview.NewPages().AddPage("main", main, true, true)
	a.tv.SetRoot(a.pages, true)
	a.initExtras()
	a.loadCmdHistory()
	a.tv.SetInputCapture(a.globalKeys)
	a.tv.SetBeforeDrawFunc(func(tcell.Screen) bool { a.beforeDraw(); return false })
	a.updateStatus()
}

func (a *App) focusDefault() { a.tv.SetFocus(a.editors) }

// pane returns the keymap pane of the focused primitive. It uses
// HasFocus rather than Application.GetFocus because it also runs inside
// the before-draw hook, where tview holds its application lock.
func (a *App) pane() string {
	switch {
	case a.tree.HasFocus() || a.treeInput.HasFocus():
		return PaneTree
	case a.editors.HasFocus():
		return PaneEditor
	case a.cmd.HasFocus():
		return PaneCmd
	}
	for _, t := range a.results.Tabs() {
		if t.primitive().HasFocus() {
			return PaneGrid
		}
	}
	return PaneGlobal
}

func (a *App) beforeDraw() {
	pane := a.pane()
	a.footer.SetLabels(a.keymap.Footer(pane))
	focusColor := func(b *tview.Box, focused bool) {
		if focused {
			b.SetBorderColor(a.theme.BorderFocus)
		} else {
			b.SetBorderColor(a.theme.Border)
		}
	}
	focusColor(a.tree.Box, pane == PaneTree)
	focusColor(a.editors.Box, pane == PaneEditor)
	focusColor(a.results.Box, pane == PaneGrid)
	a.results.Refresh()
	a.updateStatus()
}

func (a *App) updateStatus() {
	info := StatusInfo{Busy: a.busy.Load() > 0, Message: a.lastErr, IsError: a.lastErr != ""}
	if s := a.ws.Current(); s != nil {
		info.Profile = s.Profile.Name
		info.ReadOnly = s.Profile.ReadOnly
		info.Manual = !s.Profile.IsAutocommit()
		info.TxnOpen = s.TxnOpen()
	}
	if t := a.results.Current(); t.Grid != nil && t.Grid.Result() != nil {
		parts := t.Grid.StatusParts()
		info.Rows, info.Sort, info.Filter, info.Hint = parts.Rows, parts.Sort, parts.Filter, parts.Hint
		info.Duration = t.Grid.Result().Duration
	} else {
		a.mu.Lock()
		info.Duration = a.lastDur
		a.mu.Unlock()
	}
	a.status.Set(info)
	p := a.ws.Path()
	a.cmd.SetLabel(p.String() + " > ")
}

// ---------------------------------------------------------------- worker

// worker runs Lua commands and main-session statements one at a time:
// the Lua state is used from this goroutine only.
func (a *App) worker() {
	for {
		select {
		case <-a.quit:
			return
		case job := <-a.jobs:
			a.busy.Add(1)
			a.tv.QueueUpdateDraw(func() {})
			func() {
				defer func() {
					if r := recover(); r != nil {
						a.message(exec.Message{Err: fmt.Errorf("internal error: %v", r)})
					}
				}()
				job()
			}()
			a.busy.Add(-1)
			a.tv.QueueUpdateDraw(a.afterJob)
		}
	}
}

// queue runs f on the UI goroutine and redraws.
func (a *App) queue(f func()) { a.tv.QueueUpdateDraw(f) }

// do queues a job for the worker.
func (a *App) do(job func()) {
	select {
	case a.jobs <- job:
	case <-a.quit:
	}
}

// ui runs f on the UI goroutine and waits for it (call from the worker only).
func (a *App) ui(f func()) {
	done := make(chan struct{})
	a.tv.QueueUpdateDraw(func() { f(); close(done) })
	select {
	case <-done:
	case <-a.quit:
	}
}

func (a *App) afterJob() {
	p := a.ws.Path()
	if p != a.lastPath {
		a.lastPath = p
		a.tree.SelectPath(p)
		if s := a.ws.Current(); s != nil {
			a.editors.SetLexer(s.Dialect.LexerName())
			a.editors.SetSplitter(s.Dialect.Splitter())
		}
	}
	a.updateStatus()
}

// runner returns the runner of a session (one per session).
func (a *App) runner(s *session.Session) *exec.Runner {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r, ok := a.runners[s]; ok {
		return r
	}
	r := exec.NewRunner(s, &host{a})
	r.FetchSize = a.cfg.Settings.FetchSize
	r.MaxRows = a.cfg.Settings.MaxRows
	r.PageSize = a.cfg.Settings.PageSize // the grid pages (spec 10.2); max_rows does not apply
	r.MaxBuffered = a.cfg.Settings.MaxRowsBuffered
	r.AfterStatement = a.sessionHooks(s)
	r.Confirm = func(stmt string) bool {
		ok, _ := (&host{a}).Confirm("Run this statement?\n\n" + exec.Shorten(stmt, 400))
		return ok
	}
	a.runners[s] = r
	return r
}

// runCommand invokes a command by name through the registry (worker).
func (a *App) runCommand(name string, args ...string) {
	if a.reg == nil {
		return
	}
	a.clearError()
	if err := a.reg.Run(context.Background(), name, args...); err != nil {
		a.message(commandErrorMessage(err))
	}
}

// invoke queues a command by name (UI goroutine).
func (a *App) invoke(name string, args ...string) { a.do(func() { a.runCommand(name, args...) }) }

func (a *App) clearError() { a.ui(func() { a.lastErr = "" }) }

func (a *App) message(m exec.Message) { (&host{a}).Message(m) }

// ---------------------------------------------------------------- keys

func (a *App) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	if a.routePopup(ev) {
		return nil
	}
	if out, done := a.clipboardKeys(ev); done {
		return out
	}
	ev = a.fkeys.Translate(ev)
	if name, _ := a.pages.GetFrontPage(); name != "main" {
		if ev.Key() == tcell.KeyF10 && name != "splash" {
			return ev
		}
		return ev // modal windows override the global bindings
	}
	pane := a.pane()
	if a.treeInput.HasFocus() {
		return ev
	}
	if a.cmdListKey(ev) {
		return ev // the open completion list has the focus (Tab accepts, not next pane)
	}
	action := a.keymap.Action(pane, ev)
	if action == "" {
		return ev
	}
	if a.handle(pane, action) {
		return nil
	}
	return ev
}

// handle performs an action; returns false to pass the key on.
func (a *App) handle(pane, action string) bool {
	switch action {
	case "help":
		a.showHelp(pane)
	case "profiles":
		a.showProfiles()
	case "cancel":
		// F8 on a following result (tail -f) stops following.
		if res := a.results.CurrentResult(); res != nil && res.Following() {
			go func() {
				res.StopFollow()
				a.message(exec.Message{Info: res.Title + ": following stopped (F8)"})
			}()
		}
		// F8 also interrupts the Lua code of a running command (an endless
		// loop in a user script); a running statement is cancelled below.
		if a.busy.Load() > 0 && a.reg != nil {
			a.reg.Runtime().Interrupt()
		}
		if s := a.ws.Current(); s != nil && a.busy.Load() > 0 {
			go func() {
				if err := s.Cancel(context.Background()); err != nil {
					a.tv.QueueUpdateDraw(func() { a.log.Add(exec.Message{Err: fmt.Errorf("cancel: %w", err)}) })
				}
			}()
		}
	case "cmdline":
		a.tv.SetFocus(a.cmd)
	case "quit":
		a.requestQuit()
	case "maximize":
		a.toggleMaximize(pane)
	case "focus_tree":
		a.tv.SetFocus(a.tree)
	case "focus_editor":
		a.restoreIfHidden(PaneEditor)
		a.tv.SetFocus(a.editors)
	case "focus_results":
		a.restoreIfHidden(PaneGrid)
		a.tv.SetFocus(a.results.Current().primitive())
	case "next_pane", "prev_pane":
		a.cycleFocus(action == "next_pane")
	default:
		switch pane {
		case PaneTree:
			return a.treeAction(action)
		case PaneEditor:
			return a.editorAction(action)
		case PaneGrid:
			return a.gridAction(action)
		case PaneCmd:
			switch action {
			case "complete":
				a.complete = true
				a.cmd.Autocomplete()
				return true
			case "history_prev", "history_next":
				a.cmdHistoryStep(action == "history_next")
				return true
			}
			return false
		}
		return false
	}
	return true
}

func (a *App) cycleFocus(forward bool) {
	order := []tview.Primitive{a.tree, a.editors, a.results.Current().primitive(), a.cmd}
	idx := 0
	for i, p := range order {
		if p.HasFocus() || (i == 0 && a.treeInput.HasFocus()) {
			idx = i
		}
	}
	if forward {
		idx = (idx + 1) % len(order)
	} else {
		idx = (idx + len(order) - 1) % len(order)
	}
	switch order[idx] {
	case a.editors:
		a.restoreIfHidden(PaneEditor)
	case a.results.Current().primitive():
		a.restoreIfHidden(PaneGrid)
	}
	a.tv.SetFocus(order[idx])
}

// ---------------------------------------------------------------- tree

func (a *App) treeAction(action string) bool {
	ref := a.tree.Selected()
	if ref == nil {
		return action != "filter"
	}
	switch action {
	case "filter":
		a.treeBox.ResizeItem(a.treeInput, 1, 0)
		a.treeInput.SetText(a.tree.Filter())
		a.tv.SetFocus(a.treeInput)
		return true
	case "reload":
		a.meta.Invalidate(ref.Path)
		a.tree.Reload()
		return true
	case "chdir":
		p := ref.Path
		p.Object = ""
		a.invoke("cd", QuoteArg(p.String()))
		return true
	}
	isData := ref.ObjKind == meta.KindTable || ref.ObjKind == meta.KindView || ref.ObjKind == meta.KindMaterializedView
	switch ref.Kind {
	case NodeConn, NodeDatabase, NodeSchema, NodeCategory, NodeGroup:
		if action == "open" {
			a.tree.Toggle()
		}
		return true
	case NodeObject:
		name := QualifiedName(ref.Path)
		switch action {
		case "open":
			if isData {
				a.invoke("ll", name)
			} else {
				a.invoke("edit", "-t", string(ref.ObjKind), name)
			}
		case "cat":
			a.invoke("cat", "-t", string(ref.ObjKind), name)
		case "edit":
			if ref.ObjKind != meta.KindTable {
				a.invoke("edit", "-t", string(ref.ObjKind), name)
			}
		case "new_row":
			if ref.ObjKind == meta.KindTable {
				a.openForm(ref.Path, form.ModeNew, nil, nil, nil)
			}
		}
		return true
	case NodeDetail:
		if action == "cat" {
			a.invoke("cat", QualifiedName(ref.Path))
		}
		return true
	}
	return true
}

// ---------------------------------------------------------------- editor

func (a *App) editorAction(action string) bool {
	tab := a.editors.Current()
	switch action {
	case "run_statement":
		if tab == nil {
			return true
		}
		st, ok := tab.Editor.CurrentStatement()
		if !ok {
			return true
		}
		a.runSQL(tab, func(r *exec.Runner) error {
			return r.RunStatement(context.Background(), st, true)
		})
	case "run_all":
		if tab == nil {
			return true
		}
		text, line, ok := tab.Editor.Selection()
		offset := line - 1
		if !ok {
			text, offset = tab.Editor.Text(), 0
		}
		isObject := tab.Kind == editor.KindObject
		a.runSQL(tab, func(r *exec.Runner) error {
			if isObject {
				if err := a.confirmObjectReplace(r.Sess, tab); err != nil {
					return err
				}
			}
			err := r.RunScript(context.Background(), text, offset)
			if err == nil && isObject {
				a.ui(func() { tab.Editor.SetClean() })
				a.message(exec.Message{Info: "replaced " + tab.Title})
			}
			return err
		})
	case "complete":
		a.editorComplete()
	case "prev_tab":
		a.popup.Close()
		a.editors.Prev()
	case "next_tab":
		a.popup.Close()
		a.editors.Next()
	case "new_tab":
		a.newScratch()
	case "open_scratch":
		a.showScratches()
	case "rename_scratch":
		if tab != nil && tab.Kind == editor.KindScratch {
			a.renameScratch(tab.Conn, tab.File, nil)
		}
	case "close_tab":
		if tab == nil {
			return true
		}
		i := a.editors.CurrentIndex()
		if tab.Kind == editor.KindScratch {
			// A scratch is saved before it closes; if saving fails it stays.
			if err := a.editors.CloseScratch(i); err != nil {
				a.modalButtons("confirm", "The scratch cannot be closed because it could not be saved:\n\n"+err.Error(), []string{"OK"}, func(int) {})
				return true
			}
			a.ensureScratch()
			return true
		}
		closeIt := func() {
			a.editors.Close(i)
			a.ensureScratch()
		}
		if tab.Dirty() {
			a.confirmUI("Close "+tab.Title+" and discard its changes?", closeIt)
		} else {
			closeIt()
		}
	case "restore":
		if tab != nil && tab.Kind == editor.KindObject {
			tab.Restore()
		}
	default:
		return false
	}
	return true
}

// confirmObjectReplace applies spec 9.1: Run all in an object tab is a
// write (blocked in read-only profiles, confirmed where confirm_writes is
// set). On MySQL the DROP+CREATE replace drops routine privileges (A21).
func (a *App) confirmObjectReplace(s *session.Session, tab *editor.Tab) error {
	if s.Profile.ReadOnly {
		return exec.ErrReadOnly
	}
	// A21: where replacing has side effects (MySQL: DROP + CREATE is not
	// atomic and drops routine privileges) the owner is always asked, with
	// the warning; otherwise only profiles with confirm_writes ask.
	warning := ""
	if w, ok := s.Dialect.(dialect.ReplaceWarner); ok {
		kind := meta.ObjectKind("")
		if ref, ok := tab.Object.(*script.ObjectRef); ok {
			kind = ref.Kind
		}
		warning = w.ReplaceWarning(kind)
	}
	if s.Profile.ConfirmWrites || warning != "" {
		msg := "Replace " + tab.Title + " on " + s.Profile.Name + "?"
		if warning != "" {
			msg += "\n\n" + warning
		}
		ok, _ := (&host{a}).Confirm(msg)
		if !ok {
			return exec.ErrDeclined
		}
	}
	return nil
}

// runSQL runs editor SQL on the current session (worker) and jumps to the
// error line on failure.
func (a *App) runSQL(tab *editor.Tab, f func(r *exec.Runner) error) {
	a.do(func() {
		s := a.ws.Current()
		if s == nil {
			a.message(exec.Message{Err: errors.New("no connection: F2 opens the connections")})
			return
		}
		a.clearError()
		r := a.runner(s)
		start := time.Now()
		err := f(r)
		a.mu.Lock()
		a.lastDur = time.Since(start)
		a.mu.Unlock()
		var se *exec.StmtError
		if errors.As(err, &se) {
			a.ui(func() {
				tab.Editor.SetCursorLine(se.Line)
				a.lastErr = fmt.Sprintf("line %d: %v", se.Line, se.Err)
				a.results.ShowMessages()
			})
		} else if err != nil {
			a.message(exec.Message{Err: err})
		}
	})
}

// ---------------------------------------------------------------- grid

func (a *App) gridAction(action string) bool {
	tab := a.results.Current()
	switch action {
	case "prev_result":
		a.results.Prev()
		a.tv.SetFocus(a.results.Current().primitive())
		return true
	case "close_result", "close_all_results":
		if action == "close_result" {
			a.results.Close(a.results.CurrentIndex())
		} else {
			a.results.CloseAll()
		}
		a.tv.SetFocus(a.results.Current().primitive())
		a.updateStatus()
		return true
	case "next_result":
		a.results.Next()
		a.tv.SetFocus(a.results.Current().primitive())
		return true
	case "detail":
		return false // the grid's own Enter handling opens the detail
	case "new_row", "edit_row":
		return a.gridFormAction(action, tab)
	}
	if tab.Grid == nil {
		return false
	}
	if tab.Grid.Action(action) {
		return true
	}
	switch action {
	case "rerun":
		if res := tab.Grid.Result(); res != nil {
			sql, args, origin, s := res.SQL, res.Args, res.Origin, a.sessionOf(res)
			if s == nil {
				return true
			}
			g := tab.Grid
			a.do(func() {
				nr, err := a.runner(s).Grid(context.Background(), sql, origin, args...)
				a.ui(func() {
					if err != nil {
						a.lastErr = oneLine(err.Error())
						return
					}
					nr.Title = res.Title
					g.SetResult(nr)
				})
			})
		}
	case "copy_cell", "copy_row":
		var text string
		if action == "copy_cell" {
			v, ok := tab.Grid.CurrentValue()
			if !ok {
				return true
			}
			text = grid.CellDetailText(v)
		} else {
			row, ok := tab.Grid.CurrentRow()
			if !ok {
				return true
			}
			var parts []string
			for _, v := range row {
				parts = append(parts, exec.CellText(v))
			}
			text = strings.Join(parts, "\t")
		}
		a.copyToClipboard(text)
	default:
		return false
	}
	return true
}

// copyToClipboard uses OSC 52 through tcell (works over SSH; A7).
// Called on the UI goroutine.
func (a *App) copyToClipboard(text string) {
	if a.screen != nil {
		a.screen.SetClipboard([]byte(text))
		a.lastErr = ""
	}
}

// ---------------------------------------------------------------- cmdline

func (a *App) cmdDone(key tcell.Key) {
	switch key {
	case tcell.KeyEnter:
		line := strings.TrimSpace(a.cmd.GetText())
		if line == "" {
			return
		}
		a.rememberCmd(line)
		a.cmd.SetText("")
		a.do(func() {
			a.clearError()
			if a.reg == nil {
				return
			}
			if err := a.reg.Dispatch(context.Background(), line); err != nil {
				var se *exec.StmtError
				if !errors.As(err, &se) { // statement errors are logged by the runner
					a.message(commandErrorMessage(err))
				} else {
					a.ui(func() { a.lastErr = se.Error() })
				}
			}
		})
	case tcell.KeyEscape:
		a.cmd.SetText("")
		a.focusDefault()
	}
}

// cmdKeys runs after the global bindings; with the completion list open,
// tview's InputField routes Up/Down/PgUp/PgDn/Enter/Tab/Esc to the list.
// Esc then only closes the list (no done event: the line is kept).
func (a *App) cmdKeys(ev *tcell.EventKey) *tcell.EventKey {
	if a.cmdList && ev.Key() == tcell.KeyEscape {
		a.cmdList = false
	}
	return ev
}

// cmdListKey reports whether ev belongs to the open command-line
// completion list and must bypass the global and pane bindings.
func (a *App) cmdListKey(ev *tcell.EventKey) bool {
	if !a.cmdList || !a.cmd.HasFocus() {
		return false
	}
	switch ev.Key() {
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn,
		tcell.KeyEnter, tcell.KeyTab, tcell.KeyEscape:
		return true
	}
	return false
}

// cmdHistoryStep walks the command history like a shell, as a ring that
// includes the empty new line: Up from the oldest entry wraps to the
// empty line, Down from the empty line wraps to the oldest entry.
func (a *App) cmdHistoryStep(forward bool) {
	n := len(a.cmdHistory)
	if n == 0 {
		return
	}
	if forward {
		a.histPos = (a.histPos + 1) % (n + 1)
	} else {
		a.histPos = (a.histPos + n) % (n + 1)
	}
	if a.histPos == n {
		a.cmd.SetText("")
		return
	}
	a.cmd.SetText(a.cmdHistory[a.histPos])
}

// cmdComplete returns candidates only when completion was requested by
// key (spec 3.3: never automatically) or while the list requested that
// way is still open (tview calls it on every change: re-filter).
func (a *App) cmdComplete(text string) []string {
	if (!a.complete && !a.cmdList) || a.reg == nil {
		a.cmdList = false
		return nil
	}
	a.complete = false
	c := complete.CommandLine(a.reg, a.completer(), text)
	a.cmdList = len(c) > 0 // tview drops the list when there are no entries
	return c
}

// cmdCompleted accepts an entry (Enter, Tab, mouse click). Navigating only
// moves the highlight, so re-filtering keeps working on the typed text.
func (a *App) cmdCompleted(text string, _ int, source int) bool {
	if source == tview.AutocompletedNavigate {
		return false
	}
	a.cmd.SetText(text)
	a.cmdList = false
	return true
}

// ---------------------------------------------------------------- quit

func (a *App) requestQuit() {
	var dirty []string
	for _, t := range a.editors.Tabs() {
		if t.Kind == editor.KindObject && t.Dirty() {
			dirty = append(dirty, t.Title)
		}
	}
	if err := a.editors.SaveScratches(); err != nil {
		dirty = append(dirty, "scratches that could not be saved ("+err.Error()+")")
	}
	quitTxn := func() {
		if a.openTxns() == 0 {
			a.tv.Stop()
			return
		}
		// Spec 12: quitting with an open transaction asks: commit, roll
		// back, or stay.
		a.do(func() {
			for _, s := range a.ws.Sessions() {
				if s.TxnOpen() {
					if err := a.txnDecision(s, ""); err != nil {
						return
					}
				}
			}
			a.tv.QueueUpdateDraw(a.tv.Stop)
		})
	}
	if len(dirty) == 0 {
		quitTxn()
		return
	}
	a.confirmUI("Quit and discard unsaved changes in "+strings.Join(dirty, ", ")+"?", quitTxn)
}

func (a *App) openTxns() int {
	n := 0
	for _, s := range a.ws.Sessions() {
		if s.TxnOpen() {
			n++
		}
	}
	return n
}

// errStay aborts a cd or quit when the owner keeps the transaction open.
var errStay = errors.New("transaction still open: stayed")

// txnDecision asks commit / roll back / stay for an open transaction of s
// (runs on the worker). target is the database cd wants to change to, or
// "" when quitting.
func (a *App) txnDecision(s *session.Session, target string) error {
	what := "quit"
	if target != "" {
		what = "change to database " + target
	}
	ch := make(chan int, 1)
	a.tv.QueueUpdateDraw(func() {
		a.modalButtons("txn", fmt.Sprintf("A transaction is open on %s.\nCommit or roll back before you %s?", s.Profile.Name, what),
			[]string{"Commit", "Roll back", "Stay"}, func(i int) { ch <- i })
	})
	var choice int
	select {
	case choice = <-ch:
	case <-a.quit:
		return errStay
	}
	stmt := ""
	switch choice {
	case 0:
		stmt = "COMMIT"
	case 1:
		stmt = "ROLLBACK"
	default:
		return errStay
	}
	if _, err := a.runner(s).Exec(context.Background(), stmt); err != nil {
		return err
	}
	return nil
}

// Layout weights of the right column (editor : results).
const editorWeight, resultsWeight = 2, 3

// toggleMaximize gives the focused right pane (editor or results) the full
// height of the right column; again restores the split (owner request, F11).
func (a *App) toggleMaximize(pane string) {
	if a.maximized != "" {
		a.restoreSplit()
		return
	}
	// The hidden pane is removed from the layout: a zero-sized Flex item
	// would still draw its fixed-height children (the result tab bar
	// appeared in the status line).
	switch pane {
	case PaneEditor:
		a.right.RemoveItem(a.results)
	case PaneGrid:
		a.right.RemoveItem(a.editors)
	default:
		a.lastErr = "F11 maximizes the editor or the results: focus one of them first"
		return
	}
	a.maximized = pane
}

func (a *App) restoreSplit() {
	a.right.Clear()
	a.right.AddItem(a.editors, 0, editorWeight, a.editors.HasFocus()).
		AddItem(a.results, 0, resultsWeight, false)
	a.maximized = ""
}

// restoreIfHidden restores the split when the pane about to get the focus
// is hidden by the maximized other pane.
func (a *App) restoreIfHidden(pane string) {
	if a.maximized != "" && a.maximized != pane {
		a.restoreSplit()
	}
}
