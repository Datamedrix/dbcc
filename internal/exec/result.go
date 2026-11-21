// Package exec runs statements and scripts on a session and produces the
// result model and message log (spec 9.4, 10).
package exec

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"dbcc/internal/meta"
)

// Binary is a binary cell value, rendered as <BLOB n bytes>.
type Binary []byte

// Row is one record. Each cell is nil (NULL), string or Binary.
type Row []any

// ColumnInfo describes one result column.
type ColumnInfo struct {
	Name    string
	DBType  string
	Numeric bool
	Binary  bool
}

// Origin marks a table-origin result (spec 10.3).
type Origin struct {
	Object meta.Path
	SQL    string      // qualified object name
	Table  *meta.Table // optional metadata
}

// Result is one result set. Rows holds what has been loaded; Cursor is
// non-nil while more rows can be fetched.
//
// Paging (spec 10.2): with PageSize > 0 the result holds at most one page
// of PageSize rows. Fetch loads rows of the current page lazily; when the
// page is full the cursor stays open and NextPage drops the page and reads
// the next one from the cursor. Pages that cannot be read from the cursor
// (previous pages, or any page after the cursor was closed by the
// open-cursor rule) are loaded by re-running the statement from the start
// and skipping the rows before the page — only for read statements.
type Result struct {
	mu       sync.Mutex
	Title    string
	SQL      string
	Args     []any // arguments of SQL (re-runs and requeries)
	Line     int
	Columns  []ColumnInfo
	rows     []Row
	cursor   *Cursor
	Partial  bool // cursor was closed before the end (open-cursor rule)
	Capped   bool // stopped at max_rows / max_rows_buffered
	Origin   *Origin
	Duration time.Duration
	MaxRows  int
	// PageSize is the number of rows per page; 0 means no paging (the
	// result grows up to MaxRows).
	PageSize int
	// FetchSize is the chunk in which NextPage and PrevPage load the first
	// rows of a page (default 500); the rest loads lazily through Fetch.
	FetchSize int

	offset   int  // rows of the whole result before the current page
	ahead    Row  // first row of the next page, read when the page filled up
	noNext   bool // a re-run found no rows after the current page
	read     bool // the statement is a read query (Dialect.IsRead): re-runs allowed
	released bool // Release was called: a page load in flight must not refill it
	reopenFn func(ctx context.Context, work func(c *Cursor) error) error
	follow   *follower // non-nil for a following result (tail -f, follow.go)
}

// Paging errors.
var (
	ErrLastPage  = errors.New("last page")
	ErrFirstPage = errors.New("first page")
	ErrNoPaging  = errors.New("this result has no pages")
	// ErrNotRead is returned for previous pages of a statement that is not
	// a read query: it cannot be re-run.
	ErrNotRead = errors.New("previous pages are not available: the statement is not a read query")
	// ErrNotReadNext is returned for the next page of a non-read statement
	// whose cursor was closed.
	ErrNotReadNext = errors.New("further pages are not available: the cursor was closed and the statement is not a read query")
	// ErrEarlierPages is returned by Stream when the statement cannot be
	// re-run and earlier pages are no longer in memory.
	ErrEarlierPages = errors.New("the whole result is not available: the statement is not a read query and earlier pages are gone")
)

// NewResult builds a fully buffered result.
func NewResult(title string, cols []ColumnInfo, rows []Row) *Result {
	return &Result{Title: title, Columns: cols, rows: rows}
}

// SetReopen installs the re-run mechanism (the runner does this for lazy
// results): reopen executes the statement again from the start and calls
// work with the new cursor while the statement context is active (cancel
// works). read tells whether the statement may be re-run for paging.
func (r *Result) SetReopen(read bool, reopen func(ctx context.Context, work func(c *Cursor) error) error) {
	r.mu.Lock()
	r.read, r.reopenFn = read, reopen
	r.mu.Unlock()
}

// SetPageSize enables paging with n rows per page on a result that has no
// page size yet and holds at most n rows.
func (r *Result) SetPageSize(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.PageSize == 0 && n > 0 && len(r.rows) <= n {
		r.PageSize = n
	}
}

// Rows returns the loaded rows (shared slice; do not modify).
func (r *Result) Rows() []Row { r.mu.Lock(); defer r.mu.Unlock(); return r.rows }

// Len is the number of loaded rows.
func (r *Result) Len() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.rows) }

// More reports whether more rows of the current page can be fetched.
func (r *Result) More() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cursor != nil && !r.cursor.Done() && (r.PageSize <= 0 || len(r.rows) < r.PageSize)
}

// Cursor returns the open cursor or nil.
func (r *Result) Cursor() *Cursor { r.mu.Lock(); defer r.mu.Unlock(); return r.cursor }

// Page is the 1-based number of the current page (1 without paging).
func (r *Result) Page() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.PageSize <= 0 || r.follow != nil {
		return 1
	}
	return r.offset/r.PageSize + 1
}

// Offset is the number of rows of the whole result before the current page.
func (r *Result) Offset() int { r.mu.Lock(); defer r.mu.Unlock(); return r.offset }

// HasNextPage reports whether a next page may exist: while the cursor is
// not exhausted, and after the cursor was closed early until a re-run
// sees the end.
func (r *Result) HasNextPage() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.hasNextLocked() }

func (r *Result) hasNextLocked() bool {
	if r.PageSize <= 0 || r.noNext || r.follow != nil {
		return false
	}
	if r.ahead != nil {
		return true
	}
	if r.cursor != nil && !r.cursor.Done() {
		return true
	}
	return r.Partial
}

// SkipRows is the number of rows that moving to the next (next=true) or
// previous page reads and discards by re-running the statement; 0 when
// the move reads on from the open cursor or is not possible.
func (r *Result) SkipRows(next bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.PageSize <= 0 || r.follow != nil {
		return 0
	}
	if next {
		if !r.hasNextLocked() || (r.cursor != nil && !r.cursor.Done()) {
			return 0
		}
		return r.offset + r.PageSize
	}
	return max(r.offset-r.PageSize, 0)
}

func (r *Result) chunk() int {
	n := r.FetchSize
	if n <= 0 {
		n = 500
	}
	if r.PageSize > 0 && n > r.PageSize {
		n = r.PageSize
	}
	return n
}

// Fetch loads up to n more rows from the cursor, honouring the page size
// (or MaxRows without paging).
func (r *Result) Fetch(n int) (int, error) {
	r.mu.Lock()
	c := r.cursor
	have := len(r.rows)
	max := r.MaxRows
	ps := r.PageSize
	r.mu.Unlock()
	if c == nil || c.Done() {
		return 0, nil
	}
	if ps > 0 {
		if have+n > ps {
			n = ps - have
		}
		if n <= 0 {
			return 0, nil
		}
		rows, err := c.Fetch(n)
		r.mu.Lock()
		if r.cursor == c {
			r.rows = append(r.rows, rows...)
		}
		full := len(r.rows) >= ps
		r.mu.Unlock()
		if full && err == nil {
			err = r.lookahead(c)
		}
		return len(rows), err
	}
	if max > 0 && have+n > max {
		n = max - have
	}
	if n <= 0 {
		r.mu.Lock()
		r.Capped = true
		r.mu.Unlock()
		c.Close()
		return 0, nil
	}
	rows, err := c.Fetch(n)
	r.mu.Lock()
	r.rows = append(r.rows, rows...)
	if max > 0 && len(r.rows) >= max && !c.Done() {
		r.Capped = true
		r.mu.Unlock()
		c.Close()
		return len(rows), err
	}
	r.mu.Unlock()
	return len(rows), err
}

// lookahead reads the first row of the next page when the page is full,
// so that the end of the result is known exactly (HasNextPage) and the
// cursor stays open.
func (r *Result) lookahead(c *Cursor) error {
	if c.Done() {
		return nil
	}
	rows, err := c.Fetch(1)
	r.mu.Lock()
	if r.cursor == c && len(rows) == 1 {
		r.ahead = rows[0]
	}
	r.mu.Unlock()
	return err
}

// setPage makes rows (read from c, starting at offset) the current page;
// the previous page's rows are dropped. After Release it closes c instead.
func (r *Result) setPage(c *Cursor, offset int, rows []Row) {
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		c.Close()
		return
	}
	r.rows = rows
	r.offset = offset
	r.ahead = nil
	r.noNext = false
	r.cursor = c
	r.Partial = false
	r.Capped = false
	r.mu.Unlock()
}

// afterLoad reads ahead when the freshly loaded page is already full.
func (r *Result) afterLoad(c *Cursor) error {
	r.mu.Lock()
	full := r.cursor == c && len(r.rows) >= r.PageSize
	r.mu.Unlock()
	if full {
		return r.lookahead(c)
	}
	return nil
}

// NextPage drops the current page and loads the first chunk of the next
// one: from the open cursor, or — when the cursor was closed (open-cursor
// rule) — by re-running a read statement and skipping the rows up to the
// page. It returns ErrLastPage when there is no next page; the current
// page then stays.
func (r *Result) NextPage(ctx context.Context) error {
	r.mu.Lock()
	if r.follow != nil {
		r.mu.Unlock()
		return ErrFollowing
	}
	if r.PageSize <= 0 {
		r.mu.Unlock()
		return ErrNoPaging
	}
	if !r.hasNextLocked() {
		r.mu.Unlock()
		return ErrLastPage
	}
	c, ahead, ps, have, chunk := r.cursor, r.ahead, r.PageSize, len(r.rows), r.chunk()
	next := r.offset + ps
	read, reopen := r.read, r.reopenFn
	r.mu.Unlock()
	if c != nil && !c.Done() {
		var first []Row
		if ahead != nil {
			first = []Row{ahead}
		} else if have < ps {
			// The page is not fully read: skip its rest on the open cursor.
			if _, err := c.skip(ctx, ps-have); err != nil {
				return err
			}
		}
		var err error
		if !c.Done() && chunk > len(first) {
			var rows []Row
			rows, err = c.Fetch(chunk - len(first))
			first = append(first, rows...)
		}
		if len(first) == 0 {
			r.mu.Lock()
			r.noNext = true
			r.mu.Unlock()
			if err != nil {
				return err
			}
			return ErrLastPage
		}
		r.setPage(c, next, first)
		if err != nil {
			return err
		}
		return r.afterLoad(c)
	}
	if !read || reopen == nil {
		return ErrNotReadNext
	}
	return r.loadAt(ctx, next)
}

// PrevPage loads the previous page by re-running the statement from the
// start and skipping the rows before that page (read statements only).
func (r *Result) PrevPage(ctx context.Context) error {
	r.mu.Lock()
	ps, off, read, reopen, follows := r.PageSize, r.offset, r.read, r.reopenFn, r.follow != nil
	r.mu.Unlock()
	switch {
	case follows:
		return ErrFollowing
	case ps <= 0:
		return ErrNoPaging
	case off == 0:
		return ErrFirstPage
	case !read || reopen == nil:
		return ErrNotRead
	}
	return r.loadAt(ctx, max(off-ps, 0))
}

// loadAt re-runs the statement, skips off rows and loads the first chunk
// of the page that starts there.
func (r *Result) loadAt(ctx context.Context, off int) error {
	r.mu.Lock()
	reopen, chunk := r.reopenFn, r.chunk()
	r.mu.Unlock()
	return reopen(ctx, func(c *Cursor) error {
		if off > 0 {
			n, err := c.skip(ctx, off)
			if err != nil {
				return err
			}
			if n < off {
				r.mu.Lock()
				r.noNext = true
				r.mu.Unlock()
				return ErrLastPage
			}
		}
		rows, err := c.Fetch(chunk)
		if err != nil {
			return err
		}
		if len(rows) == 0 && off > 0 {
			r.mu.Lock()
			r.noNext = true
			r.mu.Unlock()
			return ErrLastPage
		}
		r.setPage(c, off, rows)
		return r.afterLoad(c)
	})
}

// Release drops the loaded rows and closes the cursor, so the memory of a
// closed result tab is freed even if something still refers to the Result.
// A following result stops polling first; Release waits for its
// goroutine to exit.
func (r *Result) Release() {
	r.StopFollow()
	r.mu.Lock()
	c := r.cursor
	r.released = true
	r.rows = nil
	r.ahead = nil
	r.cursor = nil
	r.reopenFn = nil
	r.Args = nil
	r.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// FetchAll loads rows until the end of the page, the end or the cap.
func (r *Result) FetchAll(chunk int) error {
	for r.More() {
		n, err := r.Fetch(chunk)
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	return nil
}

// markPartial is called when cursor c is closed by the open-cursor rule;
// only the result's current cursor counts (re-runs for an export use a
// cursor of their own).
func (r *Result) markPartial(c *Cursor) {
	r.mu.Lock()
	if r.cursor != nil && r.cursor == c && !c.exhaustedNow() {
		r.Partial = true
	}
	r.mu.Unlock()
}

// Status renders the row-count part of the status line: "1,204+ rows",
// "partial", "capped"; with paging "rows 3,001–6,000 · page 2 +" (+ when
// a next page may exist). A complete single page reads "42 rows".
func (r *Result) Status() string {
	r.mu.Lock()
	n := len(r.rows)
	more := r.cursor != nil && !r.cursor.Done()
	partial, capped := r.Partial, r.Capped
	ps, off := r.PageSize, r.offset
	next := r.hasNextLocked()
	f := r.follow
	var added int
	var interval time.Duration
	var active bool
	if f != nil {
		added, interval, active = f.added, f.spec.Interval, f.active
	}
	r.mu.Unlock()
	if f != nil {
		return followStatus(n, off, added, interval, active)
	}
	if ps > 0 && n > 0 && (off > 0 || next || partial) {
		s := "rows " + FormatCount(off+1) + "–" + FormatCount(off+n) + " · page " + FormatCount(off/ps+1)
		if next {
			s += " +"
		}
		if partial {
			s += " · partial"
		}
		return s
	}
	s := FormatCount(n) + " rows"
	switch {
	case capped:
		s += " capped"
	case partial:
		s += " partial"
	case more:
		s = FormatCount(n) + "+ rows"
	}
	return s
}

// followStatus is the status of a following result: "42 rows · following
// · 3 new rows · every 2 s"; after rows were dropped from the front
// "rows 1,201–4,200 · …"; "following stopped" once polling has ended.
func followStatus(n, off, added int, interval time.Duration, active bool) string {
	s := FormatCount(n) + " rows"
	if off > 0 && n > 0 {
		s = "rows " + FormatCount(off+1) + "–" + FormatCount(off+n)
	}
	news := FormatCount(added) + " new rows"
	if added == 1 {
		news = "1 new row"
	}
	if !active {
		return s + " · following stopped · " + news
	}
	return s + " · following · " + news + " · every " + strconv.FormatFloat(interval.Seconds(), 'f', -1, 64) + " s"
}

// FormatCount formats n with thousands separators.
func FormatCount(n int) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Cursor wraps an open *sql.Rows positioned on one result set.
type Cursor struct {
	mu        sync.Mutex
	rows      *sql.Rows
	cols      []ColumnInfo
	exhausted bool // reached the end of this result set
	closed    bool
	onClose   func()
	abort     func() // stops the statement on the server (see Close)
	err       error
}

// exhaustedNow reports whether the end of the result set was reached.
func (c *Cursor) exhaustedNow() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.exhausted }

// skip reads and discards up to n rows and returns how many it read.
// Cancelling ctx (or the statement) stops it.
func (c *Cursor) skip(ctx context.Context, n int) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exhausted || c.closed {
		return 0, c.err
	}
	i := 0
	for ; i < n; i++ {
		if i%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return i, err
			}
		}
		if !c.rows.Next() {
			c.exhausted = true
			c.err = c.rows.Err()
			c.closeLocked()
			break
		}
	}
	return i, c.err
}

// Done reports whether no more rows can be fetched.
func (c *Cursor) Done() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.exhausted || c.closed }

// Fetch reads up to n rows.
func (c *Cursor) Fetch(n int) ([]Row, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exhausted || c.closed {
		return nil, c.err
	}
	out := make([]Row, 0, n)
	for len(out) < n {
		if !c.rows.Next() {
			c.exhausted = true
			c.err = c.rows.Err()
			break
		}
		row, err := scanRow(c.rows, c.cols)
		if err != nil {
			c.err = err
			c.exhausted = true
			break
		}
		out = append(out, row)
	}
	if c.exhausted {
		c.closeLocked()
	}
	return out, c.err
}

// DrainGrace is how long closing a cursor with unread rows may take
// before the statement is aborted on the server. Drivers cannot stop a
// result stream on their own: closing reads and discards every remaining
// row, which takes minutes on large tables.
var DrainGrace = 200 * time.Millisecond

// Close releases the underlying rows; idempotent. If unread rows remain
// and discarding them takes longer than DrainGrace, the statement is
// aborted on the server (KILL QUERY or driver cancel) and the connection
// stays usable.
func (c *Cursor) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	abort, pending := c.abort, !c.exhausted
	if !pending || abort == nil {
		c.closeLocked()
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	done := make(chan struct{})
	go func() {
		c.mu.Lock()
		c.closeLocked()
		c.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(DrainGrace):
		abort()
		<-done
	}
	return nil
}

func (c *Cursor) closeLocked() {
	if c.closed {
		return
	}
	c.closed = true
	c.rows.Close()
	if c.onClose != nil {
		c.onClose()
	}
}

func columnInfos(rows *sql.Rows) ([]ColumnInfo, error) {
	cts, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	out := make([]ColumnInfo, len(cts))
	for i, ct := range cts {
		t := strings.ToUpper(ct.DatabaseTypeName())
		out[i] = ColumnInfo{Name: ct.Name(), DBType: t, Numeric: isNumeric(t), Binary: isBinary(t)}
	}
	return out, nil
}

func isNumeric(t string) bool {
	for _, k := range []string{"INT", "DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL", "MONEY", "SERIAL"} {
		if strings.Contains(t, k) {
			return !strings.Contains(t, "INTERVAL") && !strings.Contains(t, "POINT")
		}
	}
	return t == "YEAR"
}

func isBinary(t string) bool {
	switch t {
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "BYTEA", "IMAGE", "GEOMETRY",
		"POINT", "LINESTRING", "POLYGON", "MULTIPOINT", "MULTILINESTRING", "MULTIPOLYGON", "GEOMETRYCOLLECTION":
		return true
	}
	return false
}

func scanRow(rows *sql.Rows, cols []ColumnInfo) (Row, error) {
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	row := make(Row, len(cols))
	for i, v := range vals {
		row[i] = normalize(v, cols[i])
	}
	return row, nil
}

func normalize(v any, col ColumnInfo) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		if col.Binary {
			return Binary(append([]byte(nil), x...))
		}
		return string(x)
	case string:
		return x
	case time.Time:
		return timeText(x, col.DBType)
	case bool:
		if x {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprint(x)
	}
}

// timeText renders a driver time value by the column's type name
// (PostgreSQL and MSSQL drivers return time.Time; MySQL returns text):
// DATE as a date, TIME as a time of day, types with a time zone
// (TIMESTAMPTZ, DATETIMEOFFSET) with their UTC offset, other timestamps
// as date and time. Without a known type a value at midnight is shown as
// a date.
func timeText(x time.Time, dbType string) string {
	frac := ""
	if x.Nanosecond() != 0 {
		frac = ".999999"
	}
	t := strings.ToUpper(dbType)
	switch {
	case t == "DATE":
		return x.Format("2006-01-02")
	case t == "TIME" || t == "TIMETZ":
		return x.Format("15:04:05" + frac)
	case strings.Contains(t, "TZ") || strings.Contains(t, "OFFSET") || strings.Contains(t, "TIME ZONE"):
		return x.Format("2006-01-02 15:04:05" + frac + "-07:00")
	case t == "" && x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0:
		return x.Format("2006-01-02")
	}
	return x.Format("2006-01-02 15:04:05" + frac)
}

// CellText renders a cell for text output; NULL as "NULL".
func CellText(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case Binary:
		return fmt.Sprintf("<BLOB %d bytes>", len(x))
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// Drain reads the rest of the open cursor in chunks of n rows, ignoring
// MaxRows and the page size, and hands each chunk to fn without keeping
// the rows in the result. ctx is checked between chunks. Afterwards the
// cursor is closed and, if Drain read any rows, the result is marked
// partial: its tab keeps the rows loaded before.
func (r *Result) Drain(ctx context.Context, n int, fn func([]Row) error) error {
	if n <= 0 {
		n = 500
	}
	r.mu.Lock()
	c, ahead := r.cursor, r.ahead
	r.ahead = nil
	r.mu.Unlock()
	if c == nil {
		return nil
	}
	read := 0
	defer func() {
		c.Close()
		if read > 0 {
			r.mu.Lock()
			r.Partial = true
			r.mu.Unlock()
		}
	}()
	if ahead != nil {
		read++
		if err := fn([]Row{ahead}); err != nil {
			return err
		}
	}
	for !c.Done() {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := c.Fetch(n)
		read += len(rows)
		if len(rows) > 0 {
			if ferr := fn(rows); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Stream hands every row of the whole result to fn, in chunks of n rows,
// without keeping them (export, spec 10.7):
//
//   - a result that is completely in memory is streamed from memory;
//   - on the first page with the cursor still open, the loaded rows are
//     followed by the rest of the cursor (Drain; the tab becomes partial);
//   - otherwise a read statement is re-run from the start through the
//     runner (cancel works) and streamed to the end;
//   - a statement that cannot be re-run fails with ErrEarlierPages when
//     earlier pages are gone.
func (r *Result) Stream(ctx context.Context, n int, fn func([]Row) error) error {
	if n <= 0 {
		n = 500
	}
	r.mu.Lock()
	rows, off, partial, capped := r.rows, r.offset, r.Partial, r.Capped
	open := r.cursor != nil && !r.cursor.Done()
	// A following result exports the rows it holds (it is one page).
	complete := (off == 0 || r.follow != nil) && r.ahead == nil && !open && !partial && !capped
	read, reopen := r.read, r.reopenFn
	r.mu.Unlock()
	if complete || (off == 0 && open && !partial) {
		for i := 0; i < len(rows); i += n {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(rows[i:min(i+n, len(rows))]); err != nil {
				return err
			}
		}
		if complete {
			return nil
		}
		return r.Drain(ctx, n, fn)
	}
	if read && reopen != nil {
		return reopen(ctx, func(c *Cursor) error {
			for !c.Done() {
				if err := ctx.Err(); err != nil {
					return err
				}
				rs, err := c.Fetch(n)
				if len(rs) > 0 {
					if ferr := fn(rs); ferr != nil {
						return ferr
					}
				}
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	if off > 0 {
		return ErrEarlierPages
	}
	// Not re-runnable, first page: what is loaded plus the rest of the cursor.
	if err := fn(rows); err != nil {
		return err
	}
	return r.Drain(ctx, n, fn)
}
