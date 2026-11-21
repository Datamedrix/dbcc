// Package exec runs statements and scripts on a session and produces the
// result model and message log (spec 9.4, 10).
package exec

import (
	"context"
	"database/sql"
	"fmt"
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
type Result struct {
	mu       sync.Mutex
	Title    string
	SQL      string
	Line     int
	Columns  []ColumnInfo
	rows     []Row
	cursor   *Cursor
	Partial  bool // cursor was closed before the end (open-cursor rule)
	Capped   bool // stopped at max_rows / max_rows_buffered
	Origin   *Origin
	Duration time.Duration
	MaxRows  int
}

// NewResult builds a fully buffered result.
func NewResult(title string, cols []ColumnInfo, rows []Row) *Result {
	return &Result{Title: title, Columns: cols, rows: rows}
}

// Rows returns the loaded rows (shared slice; do not modify).
func (r *Result) Rows() []Row { r.mu.Lock(); defer r.mu.Unlock(); return r.rows }

// Len is the number of loaded rows.
func (r *Result) Len() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.rows) }

// More reports whether more rows can be fetched.
func (r *Result) More() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cursor != nil && !r.cursor.Done()
}

// Cursor returns the open cursor or nil.
func (r *Result) Cursor() *Cursor { r.mu.Lock(); defer r.mu.Unlock(); return r.cursor }

// Fetch loads up to n more rows from the cursor, honouring MaxRows.
func (r *Result) Fetch(n int) (int, error) {
	r.mu.Lock()
	c := r.cursor
	have := len(r.rows)
	max := r.MaxRows
	r.mu.Unlock()
	if c == nil || c.Done() {
		return 0, nil
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

// Release drops the loaded rows and closes the cursor, so the memory of a
// closed result tab is freed even if something still refers to the Result.
func (r *Result) Release() {
	r.mu.Lock()
	c := r.cursor
	r.rows = nil
	r.cursor = nil
	r.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// FetchAll loads rows until the end or the cap.
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

// markPartial is called when the cursor was closed by the open-cursor rule.
func (r *Result) markPartial() {
	r.mu.Lock()
	if r.cursor != nil && !r.cursor.exhausted {
		r.Partial = true
	}
	r.mu.Unlock()
}

// Status renders the row-count part of the status line: "1,204+", "partial", "capped".
func (r *Result) Status() string {
	r.mu.Lock()
	n := len(r.rows)
	more := r.cursor != nil && !r.cursor.Done()
	partial, capped := r.Partial, r.Capped
	r.mu.Unlock()
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
	err       error
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

// Close releases the underlying rows; idempotent.
func (c *Cursor) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
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
// MaxRows, and hands each chunk to fn without keeping the rows in the
// result (export streams them, spec 10.7). ctx is checked between chunks.
// Afterwards the cursor is closed and, if Drain read any rows, the result
// is marked partial: its tab keeps the rows loaded before.
func (r *Result) Drain(ctx context.Context, n int, fn func([]Row) error) error {
	if n <= 0 {
		n = 500
	}
	c := r.Cursor()
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
