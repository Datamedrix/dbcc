package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"dbcc/internal/dialect"
	"dbcc/internal/session"
	"dbcc/internal/sqltext"
)

// Message is one message-log entry (spec 9.4).
type Message struct {
	Time           time.Time
	Stmt           string // full statement text
	Line           int
	Affected       int64 // -1 when not applicable
	Returned       int   // -1 when not applicable
	Duration       time.Duration
	Warnings       []string
	Err            error
	Info           string // free text (generated statements, notes)
	ImplicitCommit bool
}

// Short returns the statement shortened to one line of at most n runes.
func (m Message) Short(n int) string { return Shorten(m.Stmt, n) }

// Shorten collapses whitespace and cuts s to n runes with an ellipsis.
func Shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if n > 0 && len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Sink receives results and messages of a run.
type Sink interface {
	Result(r *Result)
	Message(m Message)
}

// StmtError is the error of a failed statement with its line number.
type StmtError struct {
	Stmt string
	Line int
	Err  error
}

func (e *StmtError) Error() string {
	return fmt.Sprintf("line %d: %v", e.Line, e.Err)
}
func (e *StmtError) Unwrap() error { return e.Err }

// ErrReadOnly is returned by the client-side read-only guard.
var ErrReadOnly = errors.New("read-only profile: statement rejected")

// ErrDeclined is returned when the owner declines a confirmation.
var ErrDeclined = errors.New("cancelled by user")

// Runner executes statements sequentially on the main session.
type Runner struct {
	Sess *session.Session
	Sink Sink
	// FetchSize is the lazy-loading chunk; MaxRows caps lazy results;
	// MaxBuffered caps buffered results (all but the last of a script).
	FetchSize, MaxRows, MaxBuffered int
	// Confirm is asked for non-read statements when the profile has
	// confirm_writes set; nil means "no".
	Confirm func(stmt string) bool
	// AfterStatement is called for every message (history, metadata invalidation).
	AfterStatement func(m Message)
}

// NewRunner returns a runner with the spec defaults.
func NewRunner(s *session.Session, sink Sink) *Runner {
	return &Runner{Sess: s, Sink: sink, FetchSize: 500, MaxRows: 100000, MaxBuffered: 10000}
}

// RunScript splits src with the dialect splitter and runs every statement,
// stopping at the first error. lineOffset is added to statement lines
// (when src is a selection that starts further down in a buffer).
func (r *Runner) RunScript(ctx context.Context, src string, lineOffset int) error {
	stmts, err := r.Sess.Dialect.Splitter().Split(src)
	if err != nil {
		return err
	}
	for i, st := range stmts {
		st.Line += lineOffset
		count := st.Count
		if count < 1 {
			count = 1
		}
		for k := 0; k < count; k++ {
			if err := r.RunStatement(ctx, st, i == len(stmts)-1 && k == count-1); err != nil {
				return err
			}
		}
	}
	return nil
}

// Guard applies the read-only guard and confirm_writes to stmt.
func (r *Runner) Guard(stmt string) error {
	p := r.Sess.Profile
	read := r.Sess.Dialect.IsRead(stmt)
	if p.ReadOnly && !read {
		return ErrReadOnly
	}
	if p.ConfirmWrites && !read {
		if r.Confirm == nil || !r.Confirm(stmt) {
			return ErrDeclined
		}
	}
	return nil
}

// RunStatement runs one statement. When last is true and the statement
// returns a single result set, the result stays lazily loaded.
func (r *Runner) RunStatement(ctx context.Context, st sqltext.Statement, last bool) error {
	d := r.Sess.Dialect
	msg := Message{Time: time.Now(), Stmt: st.Text, Line: st.Line, Affected: -1, Returned: -1}
	fail := func(err error) error {
		msg.Err = err
		r.emit(msg)
		return &StmtError{Stmt: st.Text, Line: st.Line, Err: err}
	}
	if err := r.Guard(st.Text); err != nil {
		return fail(err)
	}
	r.Sess.CloseCursor() // open-cursor rule
	conn := r.Sess.Main()
	start := time.Now()
	implicit := d.ImplicitCommit(st.Text)
	ctx, done := r.Sess.StatementContext(ctx)
	keepOpen := false
	defer func() { done(keepOpen) }()
	began, err := r.begin(ctx, st.Text)
	if err != nil {
		return fail(err)
	}
	if began {
		msg.Info = beganInfo
	}
	failed := func(err error) error {
		r.trackFailed(ctx)
		return fail(err)
	}

	if !d.ReturnsRows(st.Text) {
		res, err := conn.ExecContext(ctx, st.Text)
		msg.Duration = time.Since(start)
		if err != nil {
			return failed(err)
		}
		if n, err := res.RowsAffected(); err == nil {
			msg.Affected = n
		}
		r.afterWrite(ctx, &msg, implicit)
		r.emit(msg)
		return nil
	}

	rows, err := conn.QueryContext(ctx, st.Text)
	if err != nil {
		msg.Duration = time.Since(start)
		return failed(err)
	}
	// Statements that may return several result sets are buffered set by
	// set (one tab each); only a single-result last statement stays lazy.
	lazy := last && !d.MultiResult(st.Text)
	set := 0
	total := 0
	for {
		cols, err := columnInfos(rows)
		if err != nil {
			rows.Close()
			return failed(err)
		}
		if len(cols) > 0 {
			set++
			title := Shorten(st.Text, 30)
			res := &Result{Title: title, SQL: st.Text, Line: st.Line, Columns: cols, MaxRows: r.MaxRows}
			cur := &Cursor{rows: rows, cols: cols}
			res.cursor = cur
			if lazy {
				closer := closerFunc(func() error { res.markPartial(); return cur.Close() })
				cur.onClose = func() { r.Sess.ReleaseCursor(closer) }
				r.Sess.SetCursor(closer)
				if _, err := res.Fetch(r.FetchSize); err != nil {
					msg.Duration = time.Since(start)
					r.Sess.CloseCursor()
					return failed(err)
				}
				res.Duration = time.Since(start)
				msg.Duration = res.Duration
				msg.Returned = res.Len()
				keepOpen = res.More()
				if keepOpen {
					r.Sink.Result(res)
					r.emitLazy(msg, began)
					return nil
				}
				// Read to the end: the result set is closed, so the
				// transaction state can be asked as for any statement.
				r.Sink.Result(res)
				r.afterWrite(ctx, &msg, implicit)
				r.emit(msg)
				return nil
			}
			// Buffered: read up to MaxBuffered rows of this set.
			res.MaxRows = r.MaxBuffered
			var ferr error
			for {
				c := cur
				if c.Done() {
					break
				}
				rs, err := c.fetchKeepOpen(r.FetchSize)
				res.rows = append(res.rows, rs...)
				if err != nil {
					ferr = err
					break
				}
				if r.MaxBuffered > 0 && len(res.rows) >= r.MaxBuffered {
					if !c.exhausted {
						res.Capped = true
					}
					break
				}
			}
			res.cursor = nil
			if ferr != nil {
				rows.Close()
				msg.Duration = time.Since(start)
				return failed(ferr)
			}
			res.Duration = time.Since(start)
			total += len(res.rows)
			r.Sink.Result(res)
		}
		if !rows.NextResultSet() {
			break
		}
	}
	err = rows.Err()
	rows.Close()
	msg.Duration = time.Since(start)
	if err != nil {
		return failed(err)
	}
	if set > 0 {
		msg.Returned = total
	}
	r.afterWrite(ctx, &msg, implicit)
	r.emit(msg)
	return nil
}

// fetchKeepOpen reads up to n rows without closing rows at the end of the
// set (the caller continues with NextResultSet).
func (c *Cursor) fetchKeepOpen(n int) ([]Row, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Row, 0, n)
	for len(out) < n {
		if !c.rows.Next() {
			c.exhausted = true
			return out, nil
		}
		row, err := scanRow(c.rows, c.cols)
		if err != nil {
			c.exhausted = true
			return out, err
		}
		out = append(out, row)
	}
	return out, nil
}

// beganInfo is the message-log note of a statement that the runner
// preceded by the dialect's BEGIN (manual transactions, spec 12).
const beganInfo = "BEGIN sent first (manual transaction)"

func (r *Runner) manual() bool { return !r.Sess.Profile.IsAutocommit() }

// begin opens a manual transaction before stmt on dialects that need an
// explicit BEGIN (dialect.TxnBeginner, PostgreSQL): only when the profile
// is not autocommit, the dialect asks for one before stmt and no
// transaction is open yet. It reports whether it sent BEGIN.
func (r *Runner) begin(ctx context.Context, stmt string) (bool, error) {
	if !r.manual() {
		return false, nil
	}
	b, ok := r.Sess.Dialect.(dialect.TxnBeginner)
	if !ok {
		return false, nil
	}
	bs := b.BeginFor(stmt)
	if bs == "" || r.txnOpenNow(ctx) {
		return false, nil
	}
	if _, err := r.Sess.Main().ExecContext(ctx, bs); err != nil {
		return false, fmt.Errorf("%s (manual transaction): %w", bs, err)
	}
	r.Sess.SetTxnOpen(true)
	return true, nil
}

// trackerContext is the context of a transaction-state query: not
// cancelled with the statement (a cancelled statement may still have
// left a transaction open), but bounded.
func trackerContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// serverTxnOpen asks the dialect's TxnTracker; ok is false when the
// dialect has none or the question failed.
func (r *Runner) serverTxnOpen(ctx context.Context) (open, ok bool) {
	tr, has := r.Sess.Dialect.(dialect.TxnTracker)
	if !has {
		return false, false
	}
	ctx, cancel := trackerContext(ctx)
	defer cancel()
	open, err := tr.TxnOpen(ctx, r.Sess.Main())
	return open, err == nil
}

// txnOpenNow is the transaction state before a statement: the server's
// answer where the dialect can give one, else the recorded state.
func (r *Runner) txnOpenNow(ctx context.Context) bool {
	if open, ok := r.serverTxnOpen(ctx); ok {
		return open
	}
	return r.Sess.TxnOpen()
}

// trackFailed updates the transaction state after a failed statement in a
// manual-transaction profile. Only a TxnTracker can tell (a failed
// statement may have opened, aborted or ended the transaction); without
// one the state stays as it was.
func (r *Runner) trackFailed(ctx context.Context) {
	if !r.manual() {
		return
	}
	if open, ok := r.serverTxnOpen(ctx); ok {
		r.Sess.SetTxnOpen(open)
	}
}

// afterWrite collects server warnings and, in manual-transaction
// profiles, updates the transaction state: from the server where the
// dialect implements dialect.TxnTracker (correct for batches like
// "INSERT …; COMMIT"), else by keyword (COMMIT/ROLLBACK close, statements
// that commit implicitly close, everything else opens).
func (r *Runner) afterWrite(ctx context.Context, msg *Message, implicit bool) {
	d := r.Sess.Dialect
	if n, err := d.WarningCount(ctx, r.Sess.Main()); err == nil && n > 0 {
		msg.Warnings, _ = d.Warnings(ctx, r.Sess.Main())
	}
	if !r.manual() {
		return
	}
	if implicit {
		msg.ImplicitCommit = true
	}
	if open, ok := r.serverTxnOpen(ctx); ok {
		r.Sess.SetTxnOpen(open)
		return
	}
	kw := sqltext.Keywords(d.Tokenizer(), msg.Stmt, 1)
	switch {
	case len(kw) > 0 && (kw[0] == "COMMIT" || kw[0] == "ROLLBACK"):
		r.Sess.SetTxnOpen(false)
	case implicit:
		r.Sess.SetTxnOpen(false)
	default:
		r.Sess.SetTxnOpen(true)
	}
}

// emitLazy emits the message of a result that keeps its cursor open. The
// server cannot be asked about the transaction while the result set is
// open, so in manual-transaction profiles the state becomes open, except
// on dialects with an explicit BEGIN that was not sent for this statement
// (SHOW, SET, …: the state stays as it was).
func (r *Runner) emitLazy(msg Message, began bool) {
	if r.manual() {
		if _, explicit := r.Sess.Dialect.(dialect.TxnBeginner); began || !explicit {
			r.Sess.SetTxnOpen(true)
		}
	}
	r.emit(msg)
}

func (r *Runner) emit(m Message) {
	if r.Sink != nil {
		r.Sink.Message(m)
	}
	if r.AfterStatement != nil {
		r.AfterStatement(m)
	}
}

// closerFunc wraps a close function in a pointer so that the session can
// compare handles (func values themselves are not comparable).
type closerHandle struct{ f func() error }

func (h *closerHandle) Close() error { return h.f() }

func closerFunc(f func() error) *closerHandle { return &closerHandle{f} }

// Query runs a query with parameters on the main session and returns a
// fully buffered result capped at MaxBuffered. No message is emitted.
func (r *Runner) Query(ctx context.Context, q string, args ...any) (*Result, error) {
	if err := r.Guard(q); err != nil {
		return nil, err
	}
	r.Sess.CloseCursor()
	ctx, done := r.Sess.StatementContext(ctx)
	defer done(false)
	// Reads issued by commands do not open a transaction themselves;
	// writes through ctx:query do, like any statement.
	if !r.Sess.Dialect.IsRead(q) {
		if _, err := r.begin(ctx, q); err != nil {
			return nil, err
		}
	}
	res, err := r.query(ctx, q, args...)
	if r.manual() {
		if open, ok := r.serverTxnOpen(ctx); ok {
			r.Sess.SetTxnOpen(open)
		}
	}
	return res, err
}

func (r *Runner) query(ctx context.Context, q string, args ...any) (*Result, error) {
	rows, err := r.Sess.Main().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := columnInfos(rows)
	if err != nil {
		return nil, err
	}
	res := &Result{Title: Shorten(q, 30), SQL: q, Columns: cols}
	cur := &Cursor{rows: rows, cols: cols}
	for !cur.exhausted {
		rs, err := cur.fetchKeepOpen(r.FetchSize)
		res.rows = append(res.rows, rs...)
		if err != nil {
			return nil, err
		}
		if r.MaxBuffered > 0 && len(res.rows) >= r.MaxBuffered {
			res.Capped = !cur.exhausted
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, rows.Close()
}

// Exec runs a statement with parameters and returns the affected rows.
// A message-log entry is emitted.
func (r *Runner) Exec(ctx context.Context, q string, args ...any) (int64, error) {
	msg := Message{Time: time.Now(), Stmt: q, Affected: -1, Returned: -1}
	if err := r.Guard(q); err != nil {
		msg.Err = err
		r.emit(msg)
		return 0, err
	}
	r.Sess.CloseCursor()
	start := time.Now()
	ctx, done := r.Sess.StatementContext(ctx)
	defer done(false)
	began, err := r.begin(ctx, q)
	if err != nil {
		msg.Err = err
		r.emit(msg)
		return 0, err
	}
	if began {
		msg.Info = beganInfo
	}
	res, err := r.Sess.Main().ExecContext(ctx, q, args...)
	msg.Duration = time.Since(start)
	if err != nil {
		r.trackFailed(ctx)
		msg.Err = err
		r.emit(msg)
		return 0, err
	}
	n, _ := res.RowsAffected()
	msg.Affected = n
	r.afterWrite(ctx, &msg, r.Sess.Dialect.ImplicitCommit(q))
	r.emit(msg)
	return n, nil
}

// Grid opens a lazily loaded result for a query (ctx:grid).
func (r *Runner) Grid(ctx context.Context, q string, origin *Origin, args ...any) (*Result, error) {
	msg := Message{Time: time.Now(), Stmt: q, Affected: -1, Returned: -1}
	if err := r.Guard(q); err != nil {
		return nil, err
	}
	r.Sess.CloseCursor()
	start := time.Now()
	ctx, done := r.Sess.StatementContext(ctx)
	keepOpen := false
	defer func() { done(keepOpen) }()
	began, err := r.begin(ctx, q)
	if err != nil {
		msg.Err, msg.Duration = err, time.Since(start)
		r.emit(msg)
		return nil, err
	}
	if began {
		msg.Info = beganInfo
	}
	rows, err := r.Sess.Main().QueryContext(ctx, q, args...)
	if err != nil {
		r.trackFailed(ctx)
		msg.Err, msg.Duration = err, time.Since(start)
		r.emit(msg)
		return nil, err
	}
	cols, err := columnInfos(rows)
	if err != nil {
		rows.Close()
		return nil, err
	}
	title := Shorten(q, 30)
	if origin != nil && origin.Object.Object != "" {
		title = origin.Object.Object
	}
	res := &Result{Title: title, SQL: q, Columns: cols, MaxRows: r.MaxRows, Origin: origin}
	cur := &Cursor{rows: rows, cols: cols}
	res.cursor = cur
	closer := closerFunc(func() error { res.markPartial(); return cur.Close() })
	cur.onClose = func() { r.Sess.ReleaseCursor(closer) }
	r.Sess.SetCursor(closer)
	if _, err := res.Fetch(r.FetchSize); err != nil {
		r.Sess.CloseCursor()
		return nil, err
	}
	res.Duration = time.Since(start)
	msg.Duration, msg.Returned = res.Duration, res.Len()
	keepOpen = res.More()
	if keepOpen {
		r.emitLazy(msg, began)
	} else {
		r.afterWrite(ctx, &msg, false)
		r.emit(msg)
	}
	return res, nil
}
