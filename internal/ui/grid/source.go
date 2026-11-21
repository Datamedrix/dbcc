package grid

import (
	"context"

	"dbcc/internal/exec"
)

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

// Pager is implemented by sources that show one page of a larger result
// (spec 10.2): Len and Row are page-relative. Page, Offset, HasNextPage
// and SkipRows are called on the UI goroutine; NextPage and PrevPage
// through the grid's Fetcher (they do database I/O).
type Pager interface {
	Page() int         // 1-based page number
	Offset() int       // rows of the whole result before the page
	HasNextPage() bool // a next page may exist
	// SkipRows is the number of rows moving to the next (or previous) page
	// reads and discards by re-running the statement; 0 when none.
	SkipRows(next bool) int
	// NextPage and PrevPage replace the page; on an error the page stays
	// (exec.ErrLastPage, exec.ErrFirstPage, exec.ErrNotRead, …).
	NextPage() error
	PrevPage() error
}

// Follower is implemented by sources that grow while they are shown
// (tail -f, spec A26): rows are appended at the end on another goroutine
// and the oldest are dropped from the front (Pager.Offset grows by their
// number). Such a source has no pages.
type Follower interface {
	Follows() bool // the source follows (or followed: still no pages)
	StopFollow()   // stop following and wait for it
}

// ResultSource adapts *exec.Result to Source, Pager and Follower.
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

// Page is the result's page number.
func (s ResultSource) Page() int { return s.R.Page() }

// Offset is the number of rows before the page.
func (s ResultSource) Offset() int { return s.R.Offset() }

// HasNextPage reports whether a next page may exist.
func (s ResultSource) HasNextPage() bool { return s.R.HasNextPage() }

// SkipRows is the number of rows a re-run skips to reach the page.
func (s ResultSource) SkipRows(next bool) int { return s.R.SkipRows(next) }

// NextPage loads the next page.
func (s ResultSource) NextPage() error { return s.R.NextPage(context.Background()) }

// PrevPage loads the previous page.
func (s ResultSource) PrevPage() error { return s.R.PrevPage(context.Background()) }

// Follows reports whether the result is a following result.
func (s ResultSource) Follows() bool { return s.R.Follows() }

// StopFollow stops a following result.
func (s ResultSource) StopFollow() { s.R.StopFollow() }
