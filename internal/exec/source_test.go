package exec

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countSource yields rows "1".."n"; with block set, Next waits for
// release (or Interrupt) before each row.
type countSource struct {
	n, i      int
	block     chan struct{} // nil: never blocks
	entered   chan struct{} // receives once a blocked Next started
	intr      chan struct{}
	intrOnce  sync.Once
	closed    atomic.Bool
	closeHits atomic.Int32
}

func newCountSource(n int) *countSource { return &countSource{n: n, intr: make(chan struct{})} }

func (s *countSource) Next(scan bool) (Row, bool, error) {
	if s.block != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.block:
		case <-s.intr:
			return nil, false, errors.New("interrupted read")
		}
	}
	if s.i >= s.n {
		return nil, false, nil
	}
	s.i++
	if !scan {
		return nil, true, nil
	}
	return Row{strconv.Itoa(s.i)}, true, nil
}

func (s *countSource) Close() error { s.closed.Store(true); s.closeHits.Add(1); return nil }
func (s *countSource) Interrupt()   { s.intrOnce.Do(func() { close(s.intr) }) }

func fileResult(total, pageSize, fetchSize int, opened *int) *Result {
	cols := []ColumnInfo{{Name: "n"}}
	var r *Result
	reopen := func(ctx context.Context, work func(c *Cursor) error) error {
		*opened++
		c := NewSourceCursor(newCountSource(total), cols)
		if err := work(c); err != nil {
			c.Close()
			return err
		}
		if r.Cursor() != c {
			c.Close()
		}
		return nil
	}
	r = NewFileResult("f", cols, NewSourceCursor(newCountSource(total), cols), &FileOrigin{Path: "/x"}, pageSize, fetchSize, reopen)
	return r
}

func firstLast(r *Result) string {
	rows := r.Rows()
	if len(rows) == 0 {
		return "-"
	}
	return rows[0][0].(string) + ".." + rows[len(rows)-1][0].(string)
}

// TestSourceCursorPaging: a RowSource gets the paging code of SQL results
// unchanged: forward on the open cursor, back by reopening and skipping.
func TestSourceCursorPaging(t *testing.T) {
	opened := 0
	r := fileResult(25, 10, 4, &opened)
	if _, err := r.Fetch(4); err != nil || firstLast(r) != "1..4" {
		t.Fatalf("first chunk %s %v", firstLast(r), err)
	}
	if err := r.FetchAll(4); err != nil || firstLast(r) != "1..10" || !r.HasNextPage() {
		t.Fatalf("page 1 %s %v", firstLast(r), err)
	}
	if err := r.NextPage(context.Background()); err != nil || firstLast(r) != "11..14" || r.Page() != 2 {
		t.Fatalf("page 2 %s %v", firstLast(r), err)
	}
	r.FetchAll(4)
	if err := r.NextPage(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.FetchAll(4)
	if firstLast(r) != "21..25" || r.HasNextPage() {
		t.Fatalf("page 3 %s next %v", firstLast(r), r.HasNextPage())
	}
	if err := r.NextPage(context.Background()); !errors.Is(err, ErrLastPage) {
		t.Errorf("after the last page: %v", err)
	}
	if err := r.PrevPage(context.Background()); err != nil || firstLast(r) != "11..14" || opened != 1 {
		t.Fatalf("back %s %v opened %d", firstLast(r), err, opened)
	}
	// Stream from page 2 reopens and reads everything.
	n := 0
	if err := r.Stream(context.Background(), 7, func(rows []Row) error { n += len(rows); return nil }); err != nil || n != 25 {
		t.Errorf("stream %d %v", n, err)
	}
	if r.Status() == "" {
		t.Error("no status")
	}
}

// TestSourceCursorCloseDuringFetch: Close from another goroutine while a
// read is in flight does not wait for it; the read ends with
// ErrCursorClosed and the source is closed exactly once. Done never
// blocks meanwhile.
func TestSourceCursorCloseDuringFetch(t *testing.T) {
	src := newCountSource(100)
	src.block = make(chan struct{})
	src.entered = make(chan struct{}, 1)
	c := NewSourceCursor(src, []ColumnInfo{{Name: "n"}})
	type res struct {
		rows []Row
		err  error
	}
	done := make(chan res, 1)
	go func() {
		rows, err := c.Fetch(10)
		done <- res{rows, err}
	}()
	<-src.entered
	if c.Done() { // must not block although Fetch holds the lock
		t.Fatal("done before close")
	}
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited for the read in flight")
	}
	select {
	case r := <-done:
		if !errors.Is(r.err, ErrCursorClosed) {
			t.Errorf("fetch error %v", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fetch did not end after Close")
	}
	if !c.Done() || !src.closed.Load() || src.closeHits.Load() != 1 {
		t.Errorf("done %v closed %v hits %d", c.Done(), src.closed.Load(), src.closeHits.Load())
	}
	// Later reads return the error, nothing more.
	if rows, err := c.Fetch(5); len(rows) != 0 || !errors.Is(err, ErrCursorClosed) {
		t.Errorf("after close: %v %v", rows, err)
	}
}

// TestSourceCursorCloseRace hammers Close against Fetch (run with -race).
func TestSourceCursorCloseRace(t *testing.T) {
	for i := 0; i < 200; i++ {
		src := newCountSource(1000)
		c := NewSourceCursor(src, []ColumnInfo{{Name: "n"}})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for !c.Done() {
				c.Fetch(7)
			}
		}()
		go func() { defer wg.Done(); c.Close() }()
		wg.Wait()
		if !c.Done() || src.closeHits.Load() != 1 {
			t.Fatalf("round %d: done %v hits %d", i, c.Done(), src.closeHits.Load())
		}
	}
}

// TestSourceCursorSkipCancel: skipping stops on a cancelled context.
func TestSourceCursorSkipCancel(t *testing.T) {
	c := NewSourceCursor(newCountSource(5000), []ColumnInfo{{Name: "n"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n, err := c.skip(ctx, 4000); !errors.Is(err, context.Canceled) || n != 0 {
		t.Errorf("skip %d %v", n, err)
	}
}

// TestMarkPartialWaitsForSQLRead: for a SQL cursor markPartial waits for a
// read in flight (a complete result must not become partial because its
// last chunk was still being read); for a source cursor it answers at once.
func TestMarkPartialWaitsForSQLRead(t *testing.T) {
	r := &Result{Title: "t"}
	c := &Cursor{} // a SQL cursor (no source)
	r.cursor = c
	c.mu.Lock() // a read in flight
	done := make(chan struct{})
	go func() { r.markPartial(c); close(done) }()
	select {
	case <-done:
		t.Fatal("markPartial did not wait for the SQL read")
	case <-time.After(50 * time.Millisecond):
	}
	c.exhausted.Store(true) // the read reached the end
	c.mu.Unlock()
	<-done
	if r.Partial {
		t.Error("complete result marked partial")
	}
	// A source cursor: no wait even while its read holds the lock.
	src := NewSourceCursor(newCountSource(1), []ColumnInfo{{Name: "n"}})
	r2 := &Result{Title: "f", cursor: src}
	src.mu.Lock()
	quick := make(chan struct{})
	go func() { r2.markPartial(src); close(quick) }()
	select {
	case <-quick:
	case <-time.After(time.Second):
		t.Fatal("markPartial waited for a source read")
	}
	src.mu.Unlock()
}

// TestSourceErrorLeavesPartial: an error from the source closes the cursor
// without exhausting it; its result is partial and has a next page.
func TestSourceErrorLeavesPartial(t *testing.T) {
	cols := []ColumnInfo{{Name: "n"}}
	src := &errSource{n: 3}
	r := NewFileResult("f", cols, NewSourceCursor(src, cols), &FileOrigin{}, 10, 5, func(context.Context, func(*Cursor) error) error { return nil })
	if _, err := r.Fetch(5); err == nil {
		t.Fatal("no error")
	}
	if r.Len() != 3 || !r.Partial || !r.HasNextPage() || r.More() {
		t.Errorf("len %d partial %v next %v more %v", r.Len(), r.Partial, r.HasNextPage(), r.More())
	}
}

type errSource struct{ n, i int }

func (s *errSource) Next(bool) (Row, bool, error) {
	if s.i >= s.n {
		return nil, false, errors.New("broken record")
	}
	s.i++
	return Row{strconv.Itoa(s.i)}, true, nil
}
func (s *errSource) Close() error { return nil }
