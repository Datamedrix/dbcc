package ui

// Wiring of the M4–M7 building blocks into the application: grid sort,
// filter and pivot (ui/grid), the row form (ui/form), completion and the
// metadata cache (ui/complete, meta), and the statement history.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/history"
	"dbcc/internal/meta"
	"dbcc/internal/script"
	"dbcc/internal/session"
	"dbcc/internal/ui/complete"
	"dbcc/internal/ui/form"
	"dbcc/internal/ui/grid"
)

// initExtras sets up history, metadata cache and completion popup.
func (a *App) initExtras() {
	if h, err := history.Open(config.StateDir()); err == nil {
		a.hist = h
	} else {
		a.log.Add(exec.Message{Info: fmt.Sprintf("warning: %v (statements are not recorded)", err)})
	}
	a.meta = meta.NewCache(complete.WorkspaceLoaders(a.ws))
	a.popup = complete.NewPopup()
	a.tv.SetAfterDrawFunc(func(s tcell.Screen) {
		if a.popup.IsOpen() {
			a.popup.Draw(s)
		}
	})
}

// sessionHooks returns the AfterStatement hook of a session's runner:
// history and DDL-driven metadata invalidation.
func (a *App) sessionHooks(s *session.Session) func(exec.Message) {
	return complete.DDLInvalidator(a.meta, s, history.Chain(a.hist.Hook(s)))
}

// completer returns a completer for the current workspace state.
func (a *App) completer() *complete.Completer { return complete.ForWorkspace(a.meta, a.ws) }

// ---------------------------------------------------------------- grid

// gridOptionsFor builds the grid options of a result: lazy fetches and
// server-side re-queries run on the worker (they use the main session).
func (a *App) gridOptionsFor(res *exec.Result) grid.Options {
	s := a.sessionOf(res)
	opts := grid.Options{
		FetchSize:        a.cfg.Settings.FetchSize,
		PageSize:         a.cfg.Settings.PageSize,
		MaxColumnWidth:   a.cfg.Settings.MaxColumnWidth,
		PivotColumnWidth: a.cfg.Settings.PivotColumnWidth,
		Fetcher:          func(fn func()) { a.do(fn) },
		UI:               a.queue,
		OnSelect:         func(row, col int, v any) { a.showDetail(v) },
		OnError: func(err error) {
			a.log.Add(exec.Message{Err: err})
			a.lastErr = oneLine(err.Error())
		},
		OnChange: a.updateStatus,
		OpenPrompt: func(p tview.Primitive, title string, w, h int, closed func()) {
			if in, ok := p.(*tview.InputField); ok && strings.HasPrefix(in.GetLabel(), "WHERE") {
				a.wireWhereCompletion(in)
			}
			box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(p, 0, 1, true)
			box.SetBorder(true).SetTitle(" " + title + " ").SetBorderColor(a.theme.BorderFocus)
			a.gridPromptClosed = closed
			a.modal("gridprompt", box, w+2, h+2)
			a.tv.SetFocus(p)
		},
		ClosePrompt: func() {
			a.gridPromptClosed = nil
			a.closeModal("gridprompt", a.results.Current().primitive())
		},
	}
	if res != nil && res.File != nil {
		// A file result (A29): an error of a background read (I/O, the
		// importer, F8) goes to the message log; it runs on the worker.
		opts.OnFetched = func(_ int, err error) {
			if err != nil && !errors.Is(err, exec.ErrCursorClosed) {
				a.message(exec.Message{Err: err})
			}
		}
	}
	if s != nil && res != nil && res.Origin != nil {
		opts.CompleteWhere = a.whereCompleter(s, res.Origin)
	}
	if s != nil {
		opts.Dialect = s.Dialect
		origin := res.Origin
		opts.Requery = func(q string, args []any) (*exec.Result, error) {
			return a.runner(s).Grid(context.Background(), q, origin, args...)
		}
	}
	return opts
}

// sessionOf returns the session a result belongs to.
func (a *App) sessionOf(res *exec.Result) *session.Session {
	if res != nil && res.File != nil {
		return nil // a file result belongs to no connection (A29)
	}
	if res != nil && res.Origin != nil && res.Origin.Object.Conn != "" {
		if s := a.ws.Sessions()[res.Origin.Object.Conn]; s != nil {
			return s
		}
	}
	return a.ws.Current()
}

// ---------------------------------------------------------------- form

// formBackend adapts a session's runner for the row form.
type formBackend struct {
	a *App
	s *session.Session
}

func (b formBackend) Exec(q string, args ...any) (int64, error) {
	return b.a.runner(b.s).Exec(context.Background(), q, args...)
}
func (b formBackend) Query(q string, args ...any) (*exec.Result, error) {
	return b.a.runner(b.s).Query(context.Background(), q, args...)
}
func (b formBackend) Grid(q string, args ...any) (*exec.Result, error) {
	return b.a.runner(b.s).Grid(context.Background(), q, nil, args...)
}
func (b formBackend) Dialect() dialect.Dialect { return b.s.Dialect }
func (b formBackend) Describe(p meta.Path) (*meta.Table, error) {
	if p.Conn == "" {
		p.Conn = b.s.Profile.Name
	}
	return b.a.meta.Table(context.Background(), p)
}

// openForm opens the row form for path (new row) or for the record row of
// res (edit). Runs on the UI goroutine; metadata is loaded on the worker.
func (a *App) openForm(p meta.Path, mode form.Mode, res *exec.Result, row exec.Row, afterSave func()) {
	if p.Conn == "" {
		if s := a.ws.Current(); s != nil {
			p.Conn = s.Profile.Name
		}
	}
	s := a.ws.Sessions()[p.Conn]
	if s == nil {
		a.lastErr = "no connection for " + p.String()
		return
	}
	if s.Profile.ReadOnly {
		a.lastErr = "read-only profile: forms are disabled"
		return
	}
	prev := a.tv.GetFocus()
	a.do(func() {
		var t *meta.Table
		var err error
		if res != nil && res.Origin != nil && res.Origin.Table != nil {
			t = res.Origin.Table
		} else {
			t, err = a.meta.Table(context.Background(), p)
		}
		if err != nil {
			a.message(exec.Message{Err: fmt.Errorf("form: %w", err)})
			return
		}
		a.ui(func() {
			opts := form.Options{
				Table:       t,
				Mode:        mode,
				Backend:     formBackend{a, s},
				BackendLogs: true,
				Async: func(job, done func()) {
					a.do(func() { job(); a.queue(done) })
				},
				OnMessage: a.log.Add,
				KeyAction: func(ev *tcell.EventKey) string { return a.keymap.Action(PaneForm, ev) },
				Theme: &form.Theme{
					Text: a.theme.Text, Dim: a.theme.Dim, Accent: a.theme.Accent, Error: a.theme.Error,
					Warn: a.theme.Warn, OK: a.theme.OK, Border: a.theme.Border, BorderFocus: a.theme.BorderFocus,
					Title: a.theme.Title,
				},
				OnClose: func(saved bool) {
					a.closeModal("form", prev)
					if saved && afterSave != nil {
						afterSave()
					}
				},
			}
			if mode == form.ModeEdit && res != nil {
				opts.Columns = res.Columns
				opts.Row = row
				if _, reason := form.CanEdit(t, res.Columns, row); reason != "" {
					a.modalButtons("confirm", "This record cannot be edited:\n\n"+reason, []string{"OK"}, func(int) {})
					return
				}
			}
			f := form.New(opts)
			_, _, w, h := a.pages.GetRect()
			a.modal("form", f, max(w*9/10, 40), max(h*9/10, 12))
		})
	})
}

// gridFormAction handles F4 (edit record) and F7 (new row) in the grid.
func (a *App) gridFormAction(action string, tab *ResultTab) bool {
	if tab.Grid == nil {
		return false
	}
	res := tab.Grid.Result()
	if res == nil || res.Origin == nil {
		a.lastErr = "forms need a table-origin result (ll, head, or the tree)"
		return true
	}
	rerun := func() { tab.Grid.Action("rerun") }
	switch action {
	case "new_row":
		a.openForm(res.Origin.Object, form.ModeNew, res, nil, rerun)
	case "edit_row":
		row, ok := tab.Grid.CurrentRow()
		if !ok {
			return true
		}
		a.openForm(res.Origin.Object, form.ModeEdit, res, row, rerun)
	}
	return true
}

// ---------------------------------------------------------------- completion

// editorComplete opens the completion popup at the editor cursor.
func (a *App) editorComplete() {
	tab := a.editors.Current()
	if tab == nil || a.ws.Current() == nil {
		a.lastErr = "completion needs a connection"
		return
	}
	a.popup.OpenForEditor(a.completer(), tab.Editor)
}

// routePopup gives the completion popup the first look at a key.
func (a *App) routePopup(ev *tcell.EventKey) bool {
	if a.popup == nil || !a.popup.IsOpen() {
		return false
	}
	if !a.editors.HasFocus() {
		a.popup.Close()
		return false
	}
	return a.popup.RouteEditorKey(ev)
}

var errNoHistory = errors.New("history is not available")

// isCtrlLetter reports Ctrl+<letter> or Ctrl+Shift+<letter>: legacy
// terminals send both as the control code, CSI-u terminals as a rune with
// ModCtrl (and ModShift).
func isCtrlLetter(ev *tcell.EventKey, letter rune) bool {
	if ev.Key() == tcell.KeyCtrlA+tcell.Key(letter-'a') && ev.Modifiers()&tcell.ModAlt == 0 {
		return true
	}
	return ev.Key() == tcell.KeyRune && ev.Modifiers()&tcell.ModCtrl != 0 && ev.Modifiers()&tcell.ModAlt == 0 &&
		unicode.ToLower(ev.Rune()) == letter
}

// clipboardKeys handles Ctrl+Shift+C / X / V (owner request) and
// keeps Ctrl+C from stopping the application (tview's default): in the
// editor it copies the selection, elsewhere it is ignored. Many terminals
// handle Ctrl+Shift+C/V themselves (copying the terminal selection,
// pasting via bracketed paste); when the keys reach dbcc they act on the
// editor. Returns the event to pass on and whether it was handled.
func (a *App) clipboardKeys(ev *tcell.EventKey) (*tcell.EventKey, bool) {
	copyKey, pasteKey, cutKey := isCtrlLetter(ev, 'c'), isCtrlLetter(ev, 'v'), isCtrlLetter(ev, 'x')
	if !copyKey && !pasteKey && !cutKey {
		return ev, false
	}
	if name, _ := a.pages.GetFrontPage(); name != "main" {
		if copyKey {
			// never let Ctrl+C stop the application from a dialog
			return tcell.NewEventKey(tcell.KeyCtrlC, 0, ev.Modifiers()), true
		}
		return ev, false
	}
	tab := a.editors.Current()
	if !a.editors.HasFocus() || tab == nil {
		if copyKey {
			a.lastErr = "Ctrl+C does not quit dbcc — F10 quits"
			return nil, true
		}
		return ev, false
	}
	if copyKey {
		if !tab.Editor.Copy() {
			a.lastErr = "nothing selected to copy"
		}
		return nil, true
	}
	if cutKey {
		if !tab.Editor.Cut() {
			a.lastErr = "nothing selected to cut"
		}
		return nil, true
	}
	tab.Editor.PasteLast()
	return nil, true
}

// whereCompleter completes the free WHERE expression of a table-origin
// result (spec 10.5): columns of the table, keywords and functions. It
// completes "SELECT * FROM <table> WHERE <text>" with the SQL completer and
// returns whole-line entries, as tview's InputField expects. Candidates
// are returned only while completion was requested (Ctrl+Space or the
// alternative key; never automatically, spec 3.3).
func (a *App) whereCompleter(s *session.Session, origin *exec.Origin) func(string) []string {
	return func(text string) []string {
		if !a.whereList {
			return nil
		}
		prefix := "SELECT * FROM " + origin.SQL + " WHERE "
		full := prefix + text
		comp := complete.New(a.meta, s.Dialect, meta.Path{Conn: s.Profile.Name, Database: origin.Object.Database, Schema: origin.Object.Schema})
		ctx, cancel := context.WithTimeout(context.Background(), complete.CompleteTimeout)
		defer cancel()
		res := comp.Complete(ctx, full, len(full))
		start := res.Start - len(prefix)
		if start < 0 || start > len(text) {
			start = len(text)
		}
		var out []string
		for _, it := range res.Items {
			out = append(out, text[:start]+it.Text)
		}
		a.whereList = len(out) > 0
		return out
	}
}

// wireWhereCompletion opens the completion list of the WHERE prompt on the
// completion keys and tracks whether it is open.
func (a *App) wireWhereCompletion(in *tview.InputField) {
	a.whereList = false
	// With an AutocompletedFunc set, tview does not insert the entry itself.
	in.SetAutocompletedFunc(func(text string, index, source int) bool {
		if source == tview.AutocompletedNavigate {
			return false // moving the highlight keeps the typed text and the list
		}
		in.SetText(text)
		a.whereList = false
		return true
	})
	prev := in.GetInputCapture()
	in.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if a.keymap.Action(PaneCmd, ev) == "complete" {
			a.whereList = true
			in.Autocomplete()
			return nil
		}
		if ev.Key() == tcell.KeyEscape && a.whereList {
			a.whereList = false // tview closes the list; the prompt stays open
		}
		if prev != nil {
			return prev(ev)
		}
		return ev
	})
}

// commandErrorMessage builds the message-log entry of a failed command;
// the Lua stack trace of a script error is shown below the error.
func commandErrorMessage(err error) exec.Message {
	m := exec.Message{Err: err}
	var ce *script.CommandError
	if errors.As(err, &ce) && strings.TrimSpace(ce.Trace) != "" {
		m.Info = "Lua stack:\n" + strings.TrimSpace(ce.Trace)
	}
	return m
}
