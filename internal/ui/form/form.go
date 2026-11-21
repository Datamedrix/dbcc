package form

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
)

// Mode selects inserting a new row or editing an existing record.
type Mode int

const (
	ModeNew Mode = iota
	ModeEdit
)

func (m Mode) String() string {
	if m == ModeEdit {
		return "edit"
	}
	return "new"
}

// Backend is the database access of the form. Every method blocks and is
// only called from jobs passed to Options.Async, never on the UI goroutine.
//
// The application implements it over exec.Runner on the current session:
// Exec → Runner.Exec (guard, confirm_writes, transaction tracking and the
// message-log entry), Query → Runner.Query, Grid → Runner.Grid (lazy),
// Describe → the metadata cache / Dialect.DescribeTable.
type Backend interface {
	Exec(sql string, args ...any) (int64, error)
	Query(sql string, args ...any) (*exec.Result, error)
	Grid(sql string, args ...any) (*exec.Result, error)
	Dialect() dialect.Dialect
	Describe(path meta.Path) (*meta.Table, error)
}

// Theme holds the colours of the form (mapped from ui.Theme by the app).
type Theme struct {
	Text, Dim, Accent, Error, Warn, OK tcell.Color
	Border, BorderFocus, Title         tcell.Color
}

// DefaultTheme uses the terminal colours and the 16 basic colours.
func DefaultTheme() *Theme {
	return &Theme{
		Text: tcell.ColorDefault, Dim: tcell.ColorGray, Accent: tcell.ColorTeal,
		Error: tcell.ColorRed, Warn: tcell.ColorYellow, OK: tcell.ColorGreen,
		Border: tcell.ColorGray, BorderFocus: tcell.ColorTeal, Title: tcell.ColorDefault,
	}
}

// Form actions (the [keys.form] action names of ui.DefaultKeymap).
const (
	ActionSave    = "save"
	ActionSaveNew = "save_new"
	ActionLookup  = "lookup"
	ActionSetNull = "set_null"
	ActionUntouch = "untouch"
	ActionCancel  = "cancel"
)

// DefaultKeyAction maps the default keys of spec 14 to form actions.
func DefaultKeyAction(ev *tcell.EventKey) string {
	switch ev.Key() {
	case tcell.KeyF2:
		return ActionSave
	case tcell.KeyF6:
		return ActionSaveNew
	case tcell.KeyF4:
		return ActionLookup
	case tcell.KeyCtrlN:
		return ActionSetNull
	case tcell.KeyCtrlU:
		return ActionUntouch
	case tcell.KeyEscape:
		return ActionCancel
	}
	return ""
}

// Options configure a Form.
type Options struct {
	// Table is the metadata of the table (required).
	Table *meta.Table
	Mode  Mode
	// Columns and Row are the record to edit (ModeEdit): the result columns
	// of a table-origin result and the row under the cursor. Check
	// CanEdit first; a form whose record cannot be edited shows the reason
	// and refuses to save.
	Columns []exec.ColumnInfo
	Row     exec.Row

	Backend Backend
	// Async runs job off the UI goroutine (on the app's DB worker) and then
	// done on the UI goroutine, followed by a redraw (QueueUpdateDraw).
	// Lazy fetches of the lookup grid also go through it. Default: both
	// synchronously (tests).
	Async func(job func(), done func())

	// OnClose is called when the form closes (save or cancel). saved
	// reports whether any statement of this form succeeded, so the caller
	// can re-run the grid.
	OnClose func(saved bool)
	// OnSaved is called after each successful INSERT/UPDATE.
	OnSaved func(mode Mode, affected int64)
	// OnMessage receives message-log entries: every statement the form
	// runs (unless BackendLogs), its bound parameters, and notes such as
	// an affected-row count other than 1.
	OnMessage func(m exec.Message)
	// BackendLogs reports that Backend.Exec already writes the statement
	// to the message log (exec.Runner does); the form then only logs the
	// bound parameters and notes, not the statement a second time.
	BackendLogs bool

	// KeyAction maps a key to a form action (ActionSave …) or "". Default
	// DefaultKeyAction; the app passes keymap.Action(ui.PaneForm, ev).
	KeyAction func(ev *tcell.EventKey) string
	// Hints is the key-hint line; default lists the default keys.
	Hints string
	Theme *Theme
	// LookupLimit caps the rows of the lookup window (default 1000).
	LookupLimit int
	// FetchSize is the lazy-loading chunk of the lookup grid (default 200).
	FetchSize int
}

// field is the field of one column.
type field struct {
	col     meta.Column
	kind    Kind
	fk      *meta.ForeignKey
	pk, uk  bool
	state   State
	value   string
	hasOrig bool
	orig    any
	input   *tview.InputField // KindText
	setCur  int               // KindSet: highlighted member
	desc    string            // KindFK: first text column of the referenced row
	origDsc string
	err     string // server message marking this field
}

// Form is the row form primitive.
type Form struct {
	*tview.Box
	opts  Options
	theme *Theme
	table *meta.Table
	mode  Mode
	key   []KeyValue

	fields []*field
	cur    int
	offset int

	msg      string
	msgColor tcell.Color
	refusal  string // edit not possible

	busy    bool // a save is running
	saved   bool // any statement succeeded
	clean   bool // nothing changed since the last successful save
	closed  bool
	confirm bool // "discard changes?" question

	popup  *tview.List     // enum drop-down
	editor *tview.TextArea // multi-line editor
	edFld  *field
	look   *lookup
}

// New creates a form.
func New(opts Options) *Form {
	if opts.Async == nil {
		opts.Async = func(job, done func()) { job(); done() }
	}
	if opts.KeyAction == nil {
		opts.KeyAction = DefaultKeyAction
	}
	if opts.Theme == nil {
		opts.Theme = DefaultTheme()
	}
	if opts.LookupLimit <= 0 {
		opts.LookupLimit = 1000
	}
	if opts.FetchSize <= 0 {
		opts.FetchSize = 200
	}
	if opts.Hints == "" {
		opts.Hints = "F2 Save  F6 Save+New  F4 Lookup/Editor  ^N NULL  ^U Untouched  Esc Cancel"
	}
	f := &Form{Box: tview.NewBox(), opts: opts, theme: opts.Theme, table: opts.Table}
	f.SetBorder(true).SetTitleAlign(tview.AlignLeft)
	f.init(opts.Mode, opts.Columns, opts.Row)
	return f
}

// init (re)builds the fields for a mode.
func (f *Form) init(mode Mode, cols []exec.ColumnInfo, row exec.Row) {
	f.mode, f.key, f.refusal = mode, nil, ""
	f.fields, f.cur, f.offset = nil, 0, 0
	f.clean, f.confirm = false, false
	t := f.table
	if t == nil {
		f.refusal = "no table metadata"
		f.updateTitle()
		return
	}
	if mode == ModeEdit {
		keyCols, reason := CanEdit(t, cols, row)
		if reason != "" {
			f.refusal = reason
		}
		for _, k := range keyCols {
			f.key = append(f.key, KeyValue{Column: k, Value: row[resultIndex(cols, k)]})
		}
	}
	for _, c := range t.Columns {
		fl := &field{col: c, kind: FieldKind(t, c), fk: fkOf(t, c.Name)}
		fl.pk, fl.uk = isKeyColumn(t, c.Name)
		if mode == ModeEdit {
			if i := resultIndex(cols, c.Name); i >= 0 && i < len(row) {
				fl.hasOrig, fl.orig = true, row[i]
			}
		}
		if fl.kind == KindText {
			fl.input = tview.NewInputField()
		}
		f.fields = append(f.fields, fl)
		f.resetField(fl)
	}
	// Start on the first editable field.
	for i, fl := range f.fields {
		if fl.kind.Editable() {
			f.cur = i
			break
		}
	}
	if f.refusal != "" {
		f.setMsg("Cannot edit: "+f.refusal, f.theme.Error)
	} else {
		f.setMsg("", f.theme.Text)
	}
	f.updateTitle()
	if mode == ModeEdit {
		f.loadFKDescriptions()
	}
}

func (f *Form) updateTitle() {
	name := "?"
	if f.table != nil {
		name = f.table.Path.Object
		if f.table.Path.Database != "" {
			name = f.table.Path.Database + "." + name
		}
	}
	if f.mode == ModeEdit {
		var parts []string
		for _, k := range f.key {
			parts = append(parts, k.Column+"="+exec.CellText(k.Value))
		}
		f.SetTitle(" Edit " + name + " (" + strings.Join(parts, ", ") + ") ")
	} else {
		f.SetTitle(" New row in " + name + " ")
	}
}

// resetField sets a field back to untouched.
func (f *Form) resetField(fl *field) {
	fl.state, fl.value, fl.err = Untouched, "", ""
	fl.desc = fl.origDsc
	if fl.input != nil {
		text := ""
		if s, ok := fl.orig.(string); ok && fl.hasOrig {
			text = s
		}
		fl.input.SetText(text)
	}
	fl.setCur = 0
}

func (f *Form) setMsg(s string, c tcell.Color) { f.msg, f.msgColor = s, c }

// Mode returns the current mode (save-and-new switches to ModeNew).
func (f *Form) Mode() Mode { return f.mode }

// Refusal returns why the record cannot be edited, or "".
func (f *Form) Refusal() string { return f.refusal }

// Message returns the text of the message line.
func (f *Form) Message() string { return f.msg }

// Closed reports whether the form has been closed.
func (f *Form) Closed() bool { return f.closed }

// Busy reports whether a save is running.
func (f *Form) Busy() bool { return f.busy }

// States returns the field states (the input of BuildInsert/BuildUpdate).
func (f *Form) States() []FieldState {
	out := make([]FieldState, 0, len(f.fields))
	for _, fl := range f.fields {
		out = append(out, FieldState{Column: fl.col.Name, State: fl.state, Value: fl.value, HasOrig: fl.hasOrig, Orig: fl.orig})
	}
	return out
}

// FieldInfo describes a field for tests and the app.
type FieldInfo struct {
	Column  string
	Kind    Kind
	State   State
	Value   string
	Desc    string // FK: first text column of the referenced row
	Error   string // server message marking the field
	Markers string // e.g. "PK NN FK DF"
}

// Fields returns the fields in column order.
func (f *Form) Fields() []FieldInfo {
	out := make([]FieldInfo, 0, len(f.fields))
	for _, fl := range f.fields {
		out = append(out, FieldInfo{Column: fl.col.Name, Kind: fl.kind, State: fl.state, Value: fl.value, Desc: fl.desc, Error: fl.err, Markers: markers(fl)})
	}
	return out
}

// Current returns the column of the focused field.
func (f *Form) Current() string {
	if f.cur < 0 || f.cur >= len(f.fields) {
		return ""
	}
	return f.fields[f.cur].col.Name
}

// FocusField moves to the field of column name; it reports whether it exists.
func (f *Form) FocusField(name string) bool {
	for i, fl := range f.fields {
		if strings.EqualFold(fl.col.Name, name) {
			f.cur = i
			return true
		}
	}
	return false
}

// dirty reports unsaved changes.
func (f *Form) dirty() bool {
	if f.clean {
		return false
	}
	for _, fl := range f.fields {
		if fl.state != Untouched {
			return true
		}
	}
	return false
}

func (f *Form) touch(fl *field) {
	fl.err = ""
	f.clean = false
}

// ---------------------------------------------------------------- values

// effective returns what a field currently stands for: its value, the
// original value (edit) or the literal default (new); null reports NULL.
func (f *Form) effective(fl *field) (v string, null bool) {
	switch fl.state {
	case Value:
		return fl.value, false
	case Null:
		return "", true
	}
	if fl.hasOrig {
		if fl.orig == nil {
			return "", true
		}
		return exec.CellText(fl.orig), false
	}
	if d := fl.col.Default; d != nil && !fl.col.DefaultExpr {
		return *d, false
	}
	return "", fl.col.Nullable
}

func (f *Form) setValue(fl *field, v string) {
	fl.state, fl.value = Value, v
	if fl.input != nil && fl.input.GetText() != v {
		fl.input.SetText(v)
	}
	f.touch(fl)
}

func (f *Form) setNull(fl *field) {
	fl.state, fl.value = Null, ""
	if fl.input != nil {
		fl.input.SetText("")
	}
	f.touch(fl)
}

// group returns the fields an action applies to: all columns of a
// foreign key for FK fields, else just the field.
func (f *Form) group(fl *field) []*field {
	if fl.kind != KindFK || fl.fk == nil {
		return []*field{fl}
	}
	var out []*field
	for _, c := range fl.fk.Columns {
		if g := f.fieldOf(c); g != nil && g.kind == KindFK {
			out = append(out, g)
		}
	}
	return out
}

func (f *Form) fieldOf(col string) *field {
	for _, fl := range f.fields {
		if strings.EqualFold(fl.col.Name, col) {
			return fl
		}
	}
	return nil
}

// ---------------------------------------------------------------- keys

// InputHandler implements tview.Primitive.
func (f *Form) InputHandler() func(ev *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return f.WrapInputHandler(func(ev *tcell.EventKey, setFocus func(p tview.Primitive)) {
		f.HandleKey(ev)
	})
}

// PasteHandler implements tview.Primitive.
func (f *Form) PasteHandler() func(text string, setFocus func(p tview.Primitive)) {
	return f.WrapPasteHandler(func(text string, setFocus func(p tview.Primitive)) {
		switch {
		case f.look != nil:
			f.look.paste(text)
		case f.editor != nil:
			f.editor.PasteHandler()(text, setFocus)
		case f.popup == nil && !f.confirm && !f.busy:
			if fl := f.curField(); fl != nil && fl.input != nil {
				before := fl.input.GetText()
				fl.input.PasteHandler()(text, setFocus)
				f.afterInput(fl, before)
			}
		}
	})
}

// Focus implements tview.Primitive. The form keeps the focus itself and
// routes keys to its fields, so inner primitives never become the
// application's focus.
func (f *Form) Focus(delegate func(p tview.Primitive)) { f.Box.Focus(delegate) }

func (f *Form) curField() *field {
	if f.cur < 0 || f.cur >= len(f.fields) {
		return nil
	}
	return f.fields[f.cur]
}

// HandleKey processes a key and reports whether it was used. The app calls
// it (or the InputHandler) while the form is the focused modal.
func (f *Form) HandleKey(ev *tcell.EventKey) bool {
	if f.closed {
		return false
	}
	if f.busy {
		return true // a save is running; keys wait
	}
	switch {
	case f.confirm:
		return f.confirmKey(ev)
	case f.look != nil:
		return f.look.handleKey(ev)
	case f.editor != nil:
		return f.editorKey(ev)
	case f.popup != nil:
		return f.popupKey(ev)
	}
	switch f.opts.KeyAction(ev) {
	case ActionSave:
		f.Save(false)
		return true
	case ActionSaveNew:
		f.Save(true)
		return true
	case ActionLookup:
		f.openDetail()
		return true
	case ActionSetNull:
		f.actSetNull()
		return true
	case ActionUntouch:
		f.actUntouch()
		return true
	case ActionCancel:
		f.Cancel()
		return true
	}
	fl := f.curField()
	switch ev.Key() {
	case tcell.KeyUp, tcell.KeyBacktab:
		f.move(-1)
		return true
	case tcell.KeyDown, tcell.KeyTab:
		f.move(1)
		return true
	case tcell.KeyPgUp:
		f.move(-10)
		return true
	case tcell.KeyPgDn:
		f.move(10)
		return true
	}
	if fl == nil {
		return false
	}
	if f.refusal != "" && f.mode == ModeEdit {
		return true
	}
	switch fl.kind {
	case KindText:
		if ev.Key() == tcell.KeyEnter {
			f.move(1)
			return true
		}
		if ev.Key() >= tcell.KeyF1 && ev.Key() <= tcell.KeyF64 {
			return false
		}
		before := fl.input.GetText()
		fl.input.InputHandler()(ev, func(tview.Primitive) {})
		f.afterInput(fl, before)
		return true
	case KindBool:
		if ev.Key() == tcell.KeyEnter || (ev.Key() == tcell.KeyRune && ev.Rune() == ' ') {
			v, null := f.effective(fl)
			nv := "1"
			if !null && parseBool(v) {
				nv = "0"
			}
			f.setValue(fl, nv)
			return true
		}
	case KindSet:
		n := len(fl.col.EnumValues)
		switch {
		case ev.Key() == tcell.KeyLeft:
			if fl.setCur > 0 {
				fl.setCur--
			}
			return true
		case ev.Key() == tcell.KeyRight:
			if fl.setCur < n-1 {
				fl.setCur++
			}
			return true
		case ev.Key() == tcell.KeyEnter || (ev.Key() == tcell.KeyRune && ev.Rune() == ' '):
			if n > 0 {
				f.toggleSet(fl, fl.col.EnumValues[fl.setCur])
			}
			return true
		}
	case KindEnum:
		switch {
		case ev.Key() == tcell.KeyEnter || (ev.Key() == tcell.KeyRune && ev.Rune() == ' '):
			f.openPopup(fl)
			return true
		case ev.Key() == tcell.KeyLeft || ev.Key() == tcell.KeyRight:
			d := 1
			if ev.Key() == tcell.KeyLeft {
				d = -1
			}
			f.cycleEnum(fl, d)
			return true
		}
	case KindFK, KindLongText:
		if ev.Key() == tcell.KeyEnter {
			f.openDetail()
			return true
		}
		if ev.Key() == tcell.KeyRune && fl.kind == KindFK {
			f.setMsg("Foreign key: F4 opens the lookup window", f.theme.Dim)
			return true
		}
	}
	return ev.Key() == tcell.KeyRune
}

// afterInput marks a text field as set when its text changed.
func (f *Form) afterInput(fl *field, before string) {
	if after := fl.input.GetText(); after != before {
		fl.state, fl.value = Value, after
		f.touch(fl)
	}
}

func (f *Form) move(d int) {
	n := len(f.fields)
	if n == 0 {
		return
	}
	c := f.cur + d
	if c < 0 {
		c = 0
	}
	if c >= n {
		c = n - 1
	}
	f.cur = c
	if fl := f.curField(); fl != nil && fl.err != "" {
		f.setMsg(fl.col.Name+": "+fl.err, f.theme.Error)
	}
}

func (f *Form) actSetNull() {
	fl := f.curField()
	if fl == nil || !fl.kind.Editable() || f.refusal != "" {
		return
	}
	for _, g := range f.group(fl) {
		f.setNull(g)
		g.desc = ""
	}
}

func (f *Form) actUntouch() {
	fl := f.curField()
	if fl == nil {
		return
	}
	for _, g := range f.group(fl) {
		f.resetField(g)
	}
	f.clean = false
}

func (f *Form) toggleSet(fl *field, member string) {
	v, _ := f.effective(fl)
	sel := map[string]bool{}
	for _, m := range splitSet(v) {
		sel[strings.ToLower(m)] = true
	}
	sel[strings.ToLower(member)] = !sel[strings.ToLower(member)]
	var out []string
	for _, m := range fl.col.EnumValues {
		if sel[strings.ToLower(m)] {
			out = append(out, m)
		}
	}
	f.setValue(fl, strings.Join(out, ","))
}

func (f *Form) cycleEnum(fl *field, d int) {
	vals := fl.col.EnumValues
	if len(vals) == 0 {
		return
	}
	v, null := f.effective(fl)
	i := indexFold(vals, v)
	switch {
	case (null || i < 0) && d > 0:
		i = 0
	case null || i < 0:
		i = len(vals) - 1
	default:
		i = (i + d + len(vals)) % len(vals)
	}
	f.setValue(fl, vals[i])
}

// openDetail opens the lookup (FK) or the multi-line editor (TEXT/JSON).
func (f *Form) openDetail() {
	fl := f.curField()
	if fl == nil || f.refusal != "" {
		return
	}
	switch fl.kind {
	case KindFK:
		f.openLookup(fl.fk)
	case KindLongText, KindText:
		f.openEditor(fl)
	case KindEnum:
		f.openPopup(fl)
	}
}

// ---------------------------------------------------------------- cancel

// Cancel closes the form, asking first if something changed.
func (f *Form) Cancel() {
	if f.dirty() {
		f.confirm = true
		f.setMsg("Discard changes? (y/n)", f.theme.Warn)
		return
	}
	f.close()
}

func (f *Form) confirmKey(ev *tcell.EventKey) bool {
	switch {
	case ev.Key() == tcell.KeyRune && (ev.Rune() == 'y' || ev.Rune() == 'Y'):
		f.confirm = false
		f.close()
	case ev.Key() == tcell.KeyRune && (ev.Rune() == 'n' || ev.Rune() == 'N'), ev.Key() == tcell.KeyEscape:
		f.confirm = false
		f.setMsg("", f.theme.Text)
	}
	return true
}

func (f *Form) close() {
	if f.closed {
		return
	}
	f.closed = true
	if f.look != nil {
		f.look.close()
	}
	if f.opts.OnClose != nil {
		f.opts.OnClose(f.saved)
	}
}

// ---------------------------------------------------------------- save

func (f *Form) message(m exec.Message) {
	if f.opts.OnMessage != nil {
		if m.Time.IsZero() {
			m.Time = time.Now()
		}
		f.opts.OnMessage(m)
	}
}

// Save runs the INSERT or UPDATE. With andNew the form becomes an empty
// new-row form for the same table afterwards; otherwise it closes. On a
// server error the form stays open with the field(s) marked.
func (f *Form) Save(andNew bool) {
	if f.busy || f.closed {
		return
	}
	if f.refusal != "" {
		f.setMsg("Cannot save: "+f.refusal, f.theme.Error)
		return
	}
	if f.opts.Backend == nil {
		f.setMsg("No connection", f.theme.Error)
		return
	}
	for _, fl := range f.fields {
		fl.err = ""
	}
	d := f.opts.Backend.Dialect()
	var (
		q    string
		args []any
		err  error
	)
	mode := f.mode
	if mode == ModeEdit {
		q, args, err = BuildUpdate(d, f.table, f.States(), f.key)
	} else {
		q, args, err = BuildInsert(d, f.table, f.States())
	}
	if errors.Is(err, ErrNoChanges) {
		f.setMsg("No changes", f.theme.Dim)
		f.finish(andNew)
		return
	}
	if err != nil {
		f.setMsg(err.Error(), f.theme.Error)
		return
	}
	f.busy = true
	f.setMsg("Saving…", f.theme.Dim)
	be := f.opts.Backend
	var (
		n     int64
		xerr  error
		start time.Time
		dur   time.Duration
	)
	f.opts.Async(func() {
		start = time.Now()
		n, xerr = be.Exec(q, args...)
		dur = time.Since(start)
	}, func() {
		f.busy = false
		f.afterSave(mode, andNew, q, args, n, xerr, dur)
	})
}

func formatArgs(args []any) string {
	parts := make([]string, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case nil:
			parts[i] = "NULL"
		case string:
			parts[i] = "'" + strings.ReplaceAll(exec.Shorten(v, 60), "'", "''") + "'"
		case []byte:
			parts[i] = fmt.Sprintf("<%d bytes>", len(v))
		default:
			parts[i] = fmt.Sprint(v)
		}
	}
	return "params: " + strings.Join(parts, ", ")
}

func (f *Form) afterSave(mode Mode, andNew bool, q string, args []any, n int64, err error, dur time.Duration) {
	if f.closed {
		return
	}
	info := ""
	if len(args) > 0 {
		info = formatArgs(args)
	}
	if !f.opts.BackendLogs {
		m := exec.Message{Stmt: q, Affected: n, Returned: -1, Duration: dur, Err: err, Info: info}
		if err != nil {
			m.Affected = -1
		}
		f.message(m)
	} else if info != "" {
		f.message(exec.Message{Affected: -1, Returned: -1, Info: "form " + info})
	}
	if err != nil {
		f.markError(err)
		return
	}
	f.saved = true
	if f.opts.OnSaved != nil {
		f.opts.OnSaved(mode, n)
	}
	if mode == ModeEdit && n != 1 {
		w := fmt.Sprintf("UPDATE affected %d rows, expected 1", n)
		if n == 0 {
			w += " (the record may have been changed or deleted meanwhile, or the new values equal the stored ones)"
		}
		f.message(exec.Message{Affected: -1, Returned: -1, Err: errors.New(w)})
		f.setMsg("WARNING: "+w, f.theme.Error)
		f.clean = true
		return
	}
	f.finish(andNew)
}

// finish closes the form or turns it into an empty new-row form.
func (f *Form) finish(andNew bool) {
	if andNew {
		f.init(ModeNew, nil, nil)
		f.setMsg("Saved. New row.", f.theme.OK)
		return
	}
	f.close()
}

// markError maps a server error to the field(s) concerned.
func (f *Form) markError(err error) {
	var fe *dialect.FieldError
	if f.opts.Backend != nil {
		fe = f.opts.Backend.Dialect().MapError(err, f.table)
	}
	if fe == nil {
		f.setMsg("Error: "+err.Error(), f.theme.Error)
		return
	}
	var cols []string
	if fe.Constraint != "" {
		cols = f.table.ConstraintColumns(fe.Constraint)
	}
	if len(cols) == 0 && fe.Column != "" {
		cols = []string{fe.Column}
	}
	first := -1
	for _, c := range cols {
		for i, fl := range f.fields {
			if strings.EqualFold(fl.col.Name, c) {
				fl.err = fe.Message
				if first < 0 {
					first = i
				}
			}
		}
	}
	if first >= 0 {
		f.cur = first
		f.setMsg(strings.Join(cols, ", ")+": "+fe.Message, f.theme.Error)
		return
	}
	f.setMsg("Error: "+fe.Message, f.theme.Error)
}

// ---------------------------------------------------------------- enum popup

func (f *Form) openPopup(fl *field) {
	l := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true)
	l.SetBorder(true).SetTitle(" " + fl.col.Name + " ").SetBorderColor(f.theme.BorderFocus)
	v, null := f.effective(fl)
	for _, e := range fl.col.EnumValues {
		l.AddItem(tview.Escape(e), "", 0, nil)
	}
	if i := indexFold(fl.col.EnumValues, v); i >= 0 && !null {
		l.SetCurrentItem(i)
	}
	f.popup = l
	f.edFld = fl
}

func (f *Form) popupKey(ev *tcell.EventKey) bool {
	switch ev.Key() {
	case tcell.KeyEscape:
		f.popup, f.edFld = nil, nil
	case tcell.KeyEnter:
		i := f.popup.GetCurrentItem()
		if fl := f.edFld; fl != nil && i >= 0 && i < len(fl.col.EnumValues) {
			f.setValue(fl, fl.col.EnumValues[i])
		}
		f.popup, f.edFld = nil, nil
	default:
		f.popup.InputHandler()(ev, func(tview.Primitive) {})
	}
	return true
}

// ---------------------------------------------------------------- editor

func (f *Form) openEditor(fl *field) {
	if !fl.kind.Editable() || fl.kind == KindFK {
		return
	}
	v, _ := f.effective(fl)
	if fl.state == Untouched && !fl.hasOrig {
		v = ""
	}
	ta := tview.NewTextArea().SetText(v, false)
	ta.SetBorder(true).SetBorderColor(f.theme.BorderFocus).
		SetTitle(" " + fl.col.Name + " " + fl.col.DataType + " — F2/F4 apply, Esc discard ").SetTitleAlign(tview.AlignLeft)
	f.editor, f.edFld = ta, fl
}

func (f *Form) editorKey(ev *tcell.EventKey) bool {
	switch a := f.opts.KeyAction(ev); {
	case a == ActionSave || a == ActionLookup:
		if fl := f.edFld; fl != nil {
			f.setValue(fl, f.editor.GetText())
		}
		f.editor, f.edFld = nil, nil
		return true
	case a == ActionCancel:
		f.editor, f.edFld = nil, nil
		return true
	}
	f.editor.InputHandler()(ev, func(tview.Primitive) {})
	return true
}

// EditorText returns the text of the open multi-line editor (tests).
func (f *Form) EditorText() (string, bool) {
	if f.editor == nil {
		return "", false
	}
	return f.editor.GetText(), true
}

// ---------------------------------------------------------------- FK descriptions

// descColumn returns the first text column of a referenced table that is
// not one of its key columns.
func descColumn(ref *meta.Table, refCols []string) string {
	if ref == nil {
		return ""
	}
	for _, c := range ref.Columns {
		if IsTextType(c.DataType) && !containsFold(refCols, c.Name) {
			return c.Name
		}
	}
	return ""
}

// loadFKDescriptions fetches the first text column of the referenced rows
// of an edited record.
func (f *Form) loadFKDescriptions() {
	be := f.opts.Backend
	if be == nil || f.table == nil {
		return
	}
	for i := range f.table.ForeignKeys {
		fk := &f.table.ForeignKeys[i]
		var vals []any
		for _, c := range fk.Columns {
			fl := f.fieldOf(c)
			if fl == nil || !fl.hasOrig || fl.orig == nil {
				vals = nil
				break
			}
			vals = append(vals, keyArg(fl.orig))
		}
		if len(vals) == 0 || len(vals) != len(fk.RefColumns) {
			continue
		}
		var desc string
		f.opts.Async(func() {
			ref, err := be.Describe(fk.RefTable)
			if err != nil {
				return
			}
			dc := descColumn(ref, fk.RefColumns)
			if dc == "" {
				return
			}
			d := be.Dialect()
			var where []string
			for j, rc := range fk.RefColumns {
				where = append(where, d.QuoteIdent(rc)+" = "+placeholder(d, j+1))
			}
			res, err := be.Query("SELECT "+d.QuoteIdent(dc)+" FROM "+d.Qualify(fk.RefTable)+" WHERE "+strings.Join(where, " AND "), vals...)
			if err != nil || res == nil || res.Len() == 0 {
				return
			}
			desc = exec.CellText(res.Rows()[0][0])
		}, func() {
			if desc == "" || f.closed || f.mode != ModeEdit {
				return
			}
			for _, c := range fk.Columns {
				if fl := f.fieldOf(c); fl != nil {
					fl.origDsc = desc
					if fl.state == Untouched {
						fl.desc = desc
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------- drawing

// markers renders the NOT NULL / key / FK / default markers.
func markers(fl *field) string {
	m := []string{"  ", "  ", "  ", "  "}
	switch {
	case fl.pk:
		m[0] = "PK"
	case fl.uk:
		m[0] = "UK"
	}
	if !fl.col.Nullable {
		m[1] = "NN"
	}
	if fl.fk != nil {
		m[2] = "FK"
	}
	if fl.col.Default != nil || fl.col.AutoIncrement || fl.col.Generated != nil {
		m[3] = "DF"
	}
	return strings.Join(m, " ")
}

// defaultText describes what an untouched field of a new row becomes.
func defaultText(c meta.Column) string {
	switch {
	case c.Default != nil && c.DefaultExpr:
		return "default " + *c.Default
	case c.Default != nil:
		return "default '" + *c.Default + "'"
	case c.Nullable:
		return "default NULL"
	}
	return "no default"
}

// display returns the text of a non-input field and its style.
func (f *Form) display(fl *field, focused bool) (string, tcell.Style) {
	st := tcell.StyleDefault.Foreground(f.theme.Text)
	dim := tcell.StyleDefault.Foreground(f.theme.Dim)
	nullSt := tcell.StyleDefault.Foreground(f.theme.Warn).Italic(true)
	v, null := f.effective(fl)
	untouchedNew := fl.state == Untouched && !fl.hasOrig
	switch fl.kind {
	case KindAuto:
		if fl.hasOrig {
			return exec.CellText(fl.orig) + "  (auto)", dim
		}
		return "auto", dim
	case KindBinary:
		if fl.hasOrig {
			return exec.CellText(fl.orig) + "  (not editable)", dim
		}
		return "(not editable) " + defaultText(fl.col), dim
	}
	if untouchedNew {
		s := defaultText(fl.col)
		switch fl.kind {
		case KindEnum:
			s += "  ▾"
		case KindFK, KindLongText:
			s += "  (F4)"
		}
		return s, dim
	}
	if null {
		s := "NULL"
		if fl.kind == KindFK || fl.kind == KindLongText {
			s += "  (F4)"
		}
		return s, nullSt
	}
	switch fl.kind {
	case KindBool:
		if parseBool(v) {
			return "[x]", st
		}
		return "[ ]", st
	case KindEnum:
		return v + "  ▾", st
	case KindFK:
		s := v
		if fl.desc != "" {
			s += " · " + fl.desc
		}
		return s, st
	case KindLongText:
		return oneLine(v), st
	}
	return v, st
}

// Draw implements tview.Primitive.
func (f *Form) Draw(screen tcell.Screen) {
	if f.HasFocus() {
		f.SetBorderColor(f.theme.BorderFocus)
	} else {
		f.SetBorderColor(f.theme.Border)
	}
	f.SetTitleColor(f.theme.Title)
	f.Box.DrawForSubclass(screen, f)
	x, y, w, h := f.GetInnerRect()
	if w <= 0 || h <= 0 {
		return
	}
	rows := h - 2
	if rows < 1 {
		rows = 1
	}
	if f.cur < f.offset {
		f.offset = f.cur
	}
	if f.cur >= f.offset+rows {
		f.offset = f.cur - rows + 1
	}
	nameW, typeW := 4, 4
	for _, fl := range f.fields {
		nameW = max(nameW, width(fl.col.Name))
		typeW = max(typeW, width(fl.col.DataType))
	}
	nameW, typeW = min(nameW, 30), min(typeW, 24)
	const markW = 11
	vx := 2 + nameW + 1 + typeW + 1 + markW + 2
	showType := true
	if w-vx < 20 {
		showType = false
		vx = 2 + nameW + 1 + markW + 2
	}
	text := tcell.StyleDefault.Foreground(f.theme.Text)
	dim := tcell.StyleDefault.Foreground(f.theme.Dim)
	overlay := f.popup != nil || f.editor != nil || f.look != nil
	for i := 0; i < rows && f.offset+i < len(f.fields); i++ {
		fl := f.fields[f.offset+i]
		yy := y + i
		focused := f.offset+i == f.cur
		mark, mst := " ", dim
		switch fl.state {
		case Value:
			mark, mst = "*", tcell.StyleDefault.Foreground(f.theme.Accent)
		case Null:
			mark, mst = "∅", tcell.StyleDefault.Foreground(f.theme.Warn)
		}
		if fl.err != "" {
			mark, mst = "!", tcell.StyleDefault.Foreground(f.theme.Error).Bold(true)
		}
		if focused {
			put(screen, x, yy, 1, "▶", tcell.StyleDefault.Foreground(f.theme.Accent))
		}
		put(screen, x+1, yy, 1, mark, mst)
		lst := text
		if fl.err != "" {
			lst = tcell.StyleDefault.Foreground(f.theme.Error).Bold(true)
		} else if focused {
			lst = tcell.StyleDefault.Foreground(f.theme.Accent).Bold(true)
		}
		cx := x + 2
		put(screen, cx, yy, nameW, clip(fl.col.Name, nameW), lst)
		cx += nameW + 1
		if showType {
			put(screen, cx, yy, typeW, clip(fl.col.DataType, typeW), dim)
			cx += typeW + 1
		}
		put(screen, cx, yy, markW, markers(fl), dim)
		vw := w - vx
		if vw <= 0 {
			continue
		}
		if fl.kind == KindText {
			f.drawInput(screen, fl, x+vx, yy, vw, focused && f.HasFocus() && !overlay)
			continue
		}
		if fl.input != nil {
			fl.input.Blur()
		}
		if fl.kind == KindSet {
			if v, null := f.effective(fl); !null || focused {
				f.drawSet(screen, fl, v, x+vx, yy, vw, focused)
				continue
			}
		}
		s, st := f.display(fl, focused)
		if focused && fl.kind.Editable() {
			st = st.Underline(true)
		}
		put(screen, x+vx, yy, vw, clip(s, vw), st)
	}
	// Message and hint lines.
	if rows < h {
		put(screen, x, y+h-2, w, clip(f.msg, w), tcell.StyleDefault.Foreground(f.msgColor))
	}
	put(screen, x, y+h-1, w, clip(f.opts.Hints, w), dim)

	switch {
	case f.look != nil:
		f.look.draw(screen, x, y, w, h)
	case f.editor != nil:
		f.editor.SetRect(x+2, y+1, max(w-4, 10), max(h-3, 3))
		f.editor.Focus(nil)
		f.editor.Draw(screen)
	case f.popup != nil:
		n := len(f.edFld.col.EnumValues)
		pw := 10
		for _, e := range f.edFld.col.EnumValues {
			pw = max(pw, width(e)+4)
		}
		ph := min(n+2, h)
		py := y + (f.cur - f.offset) + 1
		if py+ph > y+h {
			py = max(y, y+h-ph)
		}
		px := min(x+vx, x+max(w-pw, 0))
		f.popup.SetRect(px, py, min(pw, w), ph)
		f.popup.Focus(nil)
		f.popup.Draw(screen)
	}
}

func (f *Form) drawInput(screen tcell.Screen, fl *field, x, y, w int, focused bool) {
	in := fl.input
	fieldSt := tcell.StyleDefault.Foreground(f.theme.Text)
	if focused {
		fieldSt = fieldSt.Underline(true)
	}
	in.SetFieldStyle(fieldSt)
	in.SetLabel("")
	switch {
	case fl.state == Null || (fl.state == Untouched && fl.hasOrig && fl.orig == nil):
		in.SetPlaceholder("NULL")
		in.SetPlaceholderStyle(tcell.StyleDefault.Foreground(f.theme.Warn).Italic(true))
	case fl.state == Untouched && !fl.hasOrig:
		in.SetPlaceholder(defaultText(fl.col))
		in.SetPlaceholderStyle(tcell.StyleDefault.Foreground(f.theme.Dim))
	default:
		in.SetPlaceholder("")
	}
	in.SetRect(x, y, w, 1)
	if focused {
		in.Focus(nil)
	} else {
		in.Blur()
	}
	in.Draw(screen)
}

func (f *Form) drawSet(screen tcell.Screen, fl *field, v string, x, y, w int, focused bool) {
	sel := map[string]bool{}
	for _, m := range splitSet(v) {
		sel[strings.ToLower(m)] = true
	}
	cx := x
	for i, m := range fl.col.EnumValues {
		box := "[ ] "
		if sel[strings.ToLower(m)] {
			box = "[x] "
		}
		st := tcell.StyleDefault.Foreground(f.theme.Text)
		if focused && i == fl.setCur {
			st = st.Reverse(true)
		}
		cx += put(screen, cx, y, x+w-cx, box+m, st)
		cx += put(screen, cx, y, x+w-cx, "  ", tcell.StyleDefault)
		if cx >= x+w {
			break
		}
	}
}
