package grid

import "dbcc/internal/exec"

// Source is what the grid displays. *exec.Result is adapted by
// ResultSource; tests use fakes.
//
// Columns, Len, Row, More and Status are called on the UI goroutine. Fetch
// is called through the grid's Fetcher, possibly on another goroutine, so
// implementations must be safe for Len/Row/More/Status running concurrently
// with Fetch. Row(i) must be O(1) and valid for every i < Len().
type Source interface {
	Columns() []exec.ColumnInfo
	Len() int
	Row(i int) exec.Row
	More() bool
	Fetch(n int) (int, error)
	Status() string
}

// ResultSource adapts *exec.Result to Source.
type ResultSource struct{ R *exec.Result }

// Columns returns the result's columns.
func (s ResultSource) Columns() []exec.ColumnInfo { return s.R.Columns }

// Len returns the number of loaded rows.
func (s ResultSource) Len() int { return s.R.Len() }

// Row returns loaded row i. Rows() returns the shared slice under the
// result's lock, so this is O(1).
func (s ResultSource) Row(i int) exec.Row {
	rows := s.R.Rows()
	if i < 0 || i >= len(rows) {
		return nil
	}
	return rows[i]
}

// More reports whether more rows can be fetched.
func (s ResultSource) More() bool { return s.R.More() }

// Fetch loads up to n more rows.
func (s ResultSource) Fetch(n int) (int, error) { return s.R.Fetch(n) }

// Status renders the row count for the status line.
func (s ResultSource) Status() string { return s.R.Status() }
