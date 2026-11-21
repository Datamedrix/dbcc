package exec

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unsafe"
	"weak"
)

// A minimal database/sql driver: "gen N" returns the rows (id, pad) for
// id = 1..N, pad being a 64-character string allocated per row.

type fakeDriver struct{ queries atomic.Int64 }

var fakeDrv = &fakeDriver{}

func init() { sql.Register("dbcc-fake", fakeDrv) }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return fakeConn{d}, nil }

type fakeConn struct{ d *fakeDriver }

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("no transactions") }
func (c fakeConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(q, "gen "))
	if err != nil {
		return nil, fmt.Errorf("bad query %q", q)
	}
	c.d.queries.Add(1)
	return &fakeRows{n: n}, nil
}

type fakeRows struct{ i, n int }

func (*fakeRows) Columns() []string { return []string{"id", "pad"} }
func (*fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= r.n {
		return io.EOF
	}
	r.i++
	dest[0] = int64(r.i)
	dest[1] = fmt.Sprintf("%064d", r.i)
	return nil
}

// fakeSession stands in for the runner and session: one open cursor that
// a new statement closes (open-cursor rule), re-runs through reopen.
type fakeSession struct {
	t      *testing.T
	db     *sql.DB
	res    *Result
	cur    *Cursor
	reruns int
}

func (s *fakeSession) open(ctx context.Context, q string) *Cursor {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		s.t.Fatal(err)
	}
	cols, err := columnInfos(rows)
	if err != nil {
		s.t.Fatal(err)
	}
	return &Cursor{rows: rows, cols: cols}
}

// closeCursor is a new statement: the open cursor is closed, its result
// marked partial.
func (s *fakeSession) closeCursor() {
	if c := s.cur; c != nil {
		s.cur = nil
		s.res.markPartial(c)
		c.Close()
	}
}

func newPaged(t *testing.T, total, pageSize, fetchSize int, read bool) *fakeSession {
	t.Helper()
	db, err := sql.Open("dbcc-fake", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &fakeSession{t: t, db: db}
	q := "gen " + strconv.Itoa(total)
	c := s.open(context.Background(), q)
	s.cur = c
	res := &Result{SQL: q, Columns: c.cols, PageSize: pageSize, FetchSize: fetchSize, cursor: c}
	s.res = res
	res.SetReopen(read, func(ctx context.Context, work func(c *Cursor) error) error {
		s.closeCursor()
		s.reruns++
		c := s.open(ctx, q)
		s.cur = c
		if err := work(c); err != nil {
			c.Close()
			return err
		}
		return nil
	})
	if _, err := res.Fetch(fetchSize); err != nil {
		t.Fatal(err)
	}
	return s
}

func ids(res *Result) (first, last string, n int) {
	rows := res.Rows()
	if len(rows) == 0 {
		return "", "", 0
	}
	return rows[0][0].(string), rows[len(rows)-1][0].(string), len(rows)
}

func wantPage(t *testing.T, res *Result, page int, first, last string, n int) {
	t.Helper()
	if err := res.FetchAll(500); err != nil {
		t.Fatal(err)
	}
	f, l, got := ids(res)
	if res.Page() != page || f != first || l != last || got != n {
		t.Fatalf("page %d [%s..%s] %d rows; want page %d [%s..%s] %d rows", res.Page(), f, l, got, page, first, last, n)
	}
}

func TestPagingForwardReleasesPreviousPage(t *testing.T) {
	s := newPaged(t, 7500, 3000, 500, true)
	res := s.res
	if res.Len() != 500 || !res.More() {
		t.Fatalf("first chunk: %d more=%v", res.Len(), res.More())
	}
	// A weak pointer to the backing bytes of a page-1 string.
	pad := res.Rows()[1][1].(string)
	weakPad := weak.Make(unsafe.StringData(pad))
	pad = ""
	wantPage(t, res, 1, "1", "3000", 3000)
	if res.More() || !res.HasNextPage() || res.Cursor().Done() {
		t.Fatalf("full page: more=%v next=%v cursor done=%v", res.More(), res.HasNextPage(), res.Cursor().Done())
	}
	if got := res.Status(); got != "rows 1–3,000 · page 1 +" {
		t.Errorf("status %q", got)
	}
	if err := res.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res.Len() != 500 || res.Offset() != 3000 {
		t.Fatalf("page 2 first chunk: %d rows at %d", res.Len(), res.Offset())
	}
	for i := 0; i < 5 && weakPad.Value() != nil; i++ {
		runtime.GC()
	}
	if weakPad.Value() != nil {
		t.Fatal("page 1 is still reachable after NextPage (memory not released)")
	}
	wantPage(t, res, 2, "3001", "6000", 3000)
	if got := res.Status(); got != "rows 3,001–6,000 · page 2 +" {
		t.Errorf("status %q", got)
	}
	if err := res.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantPage(t, res, 3, "6001", "7500", 1500)
	if res.HasNextPage() {
		t.Error("last page reports a next page")
	}
	if got := res.Status(); got != "rows 6,001–7,500 · page 3" {
		t.Errorf("status %q", got)
	}
	if err := res.NextPage(context.Background()); !errors.Is(err, ErrLastPage) {
		t.Fatalf("NextPage on the last page: %v", err)
	}
	wantPage(t, res, 3, "6001", "7500", 1500) // the page stays
	if s.reruns != 0 {
		t.Errorf("forward paging re-ran the statement %d times", s.reruns)
	}
}

func TestPagingBackwardRerunsAndSkips(t *testing.T) {
	s := newPaged(t, 7500, 3000, 500, true)
	res := s.res
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := res.NextPage(ctx); err != nil {
			t.Fatal(err)
		}
	}
	wantPage(t, res, 3, "6001", "7500", 1500)
	if n := res.SkipRows(false); n != 3000 {
		t.Errorf("SkipRows(prev) = %d", n)
	}
	if err := res.PrevPage(ctx); err != nil {
		t.Fatal(err)
	}
	if s.reruns != 1 || res.Partial {
		t.Fatalf("reruns %d partial %v", s.reruns, res.Partial)
	}
	wantPage(t, res, 2, "3001", "6000", 3000)
	if !res.HasNextPage() {
		t.Fatal("page 2 after re-run: no next page")
	}
	// Forward again reads on from the re-run cursor.
	if err := res.NextPage(ctx); err != nil {
		t.Fatal(err)
	}
	wantPage(t, res, 3, "6001", "7500", 1500)
	if s.reruns != 1 {
		t.Errorf("reruns %d", s.reruns)
	}
	for _, want := range []int{2, 1} {
		if err := res.PrevPage(ctx); err != nil {
			t.Fatal(err)
		}
		if res.Page() != want {
			t.Fatalf("page %d, want %d", res.Page(), want)
		}
	}
	wantPage(t, res, 1, "1", "3000", 3000)
	if err := res.PrevPage(ctx); !errors.Is(err, ErrFirstPage) {
		t.Fatalf("PrevPage on page 1: %v", err)
	}
}

func TestPagingRefusesRerunOfNonRead(t *testing.T) {
	s := newPaged(t, 7500, 3000, 500, false)
	res := s.res
	ctx := context.Background()
	if err := res.NextPage(ctx); err != nil {
		t.Fatal(err) // forward from the open cursor works
	}
	err := res.PrevPage(ctx)
	if !errors.Is(err, ErrNotRead) || err.Error() != "previous pages are not available: the statement is not a read query" {
		t.Fatalf("PrevPage: %v", err)
	}
	s.closeCursor()
	if err := res.NextPage(ctx); !errors.Is(err, ErrNotReadNext) {
		t.Fatalf("NextPage after close: %v", err)
	}
	if s.reruns != 0 {
		t.Errorf("reruns %d", s.reruns)
	}
	wantPage(t, res, 2, "3001", "3500", 500) // the page stays
}

func TestPagingAfterCursorClosedByNewStatement(t *testing.T) {
	s := newPaged(t, 7500, 3000, 500, true)
	res := s.res
	wantPage(t, res, 1, "1", "3000", 3000)
	s.closeCursor() // a new statement
	if !res.Partial || !res.HasNextPage() {
		t.Fatalf("partial %v next %v", res.Partial, res.HasNextPage())
	}
	if got := res.Status(); got != "rows 1–3,000 · page 1 + · partial" {
		t.Errorf("status %q", got)
	}
	if n := res.SkipRows(true); n != 3000 {
		t.Errorf("SkipRows(next) = %d", n)
	}
	if err := res.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.reruns != 1 || res.Partial {
		t.Fatalf("reruns %d partial %v", s.reruns, res.Partial)
	}
	wantPage(t, res, 2, "3001", "6000", 3000)
	// Closed in the middle of a page: the next page starts after the page.
	if err := res.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.closeCursor()
	if err := res.NextPage(context.Background()); !errors.Is(err, ErrLastPage) {
		t.Fatalf("beyond the end after re-run: %v", err)
	}
	if res.HasNextPage() {
		t.Error("re-run saw the end, but a next page is reported")
	}
	if f, _, n := ids(res); f != "6001" || n != 500 {
		t.Errorf("page kept: %s %d", f, n)
	}
}

func TestPagingLastPageExactlyFull(t *testing.T) {
	s := newPaged(t, 6000, 3000, 500, true)
	res := s.res
	wantPage(t, res, 1, "1", "3000", 3000)
	if err := res.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantPage(t, res, 2, "3001", "6000", 3000)
	if res.HasNextPage() {
		t.Error("exactly full last page reports a next page")
	}
	if err := res.NextPage(context.Background()); !errors.Is(err, ErrLastPage) {
		t.Fatal(err)
	}
}

func TestPagingNextFromPartlyLoadedPage(t *testing.T) {
	s := newPaged(t, 7500, 3000, 500, true)
	res := s.res
	if err := res.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantPage(t, res, 2, "3001", "6000", 3000)
	if s.reruns != 0 {
		t.Errorf("reruns %d", s.reruns)
	}
}

func TestPagingSmallResult(t *testing.T) {
	s := newPaged(t, 42, 3000, 500, true)
	res := s.res
	if res.HasNextPage() || res.More() || res.Status() != "42 rows" {
		t.Fatalf("next %v more %v status %q", res.HasNextPage(), res.More(), res.Status())
	}
	if err := res.NextPage(context.Background()); !errors.Is(err, ErrLastPage) {
		t.Fatal(err)
	}
}

func TestStreamWholeResult(t *testing.T) {
	collect := func(res *Result) []string {
		var out []string
		if err := res.Stream(context.Background(), 1000, func(rows []Row) error {
			for _, r := range rows {
				out = append(out, r[0].(string))
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	check := func(what string, got []string) {
		t.Helper()
		if len(got) != 7500 {
			t.Fatalf("%s: %d rows", what, len(got))
		}
		for i, v := range got {
			if v != strconv.Itoa(i+1) {
				t.Fatalf("%s: row %d = %s", what, i, v)
			}
		}
	}
	// Page 1, cursor open: loaded rows, then the rest of the cursor.
	s := newPaged(t, 7500, 3000, 500, true)
	s.res.FetchAll(500)
	check("page 1", collect(s.res))
	if s.reruns != 0 {
		t.Errorf("page 1 export re-ran %d times", s.reruns)
	}
	// Page 2: re-run from the start.
	s = newPaged(t, 7500, 3000, 500, true)
	if err := s.res.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	check("page 2", collect(s.res))
	if s.reruns != 1 || s.res.Page() != 2 {
		t.Errorf("reruns %d page %d", s.reruns, s.res.Page())
	}
	// Not re-runnable, page 2: refused.
	s = newPaged(t, 7500, 3000, 500, false)
	if err := s.res.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.res.Stream(context.Background(), 1000, func([]Row) error { return nil }); !errors.Is(err, ErrEarlierPages) {
		t.Fatalf("stream non-read page 2: %v", err)
	}
}
