package exec

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- a fake database/sql driver: table (id, msg) ----
//
// "NEXT" returns the rows with id > the argument, ascending; "INIT" the
// last two rows ascending; "ALL" every row. Every query is recorded.

type followDB struct {
	mu      sync.Mutex
	rows    [][]driver.Value
	queries []string // "NEXT 5"
	fail    error
	block   bool // queries wait for their context
}

func (db *followDB) insert(ids ...int64) {
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, id := range ids {
		db.rows = append(db.rows, []driver.Value{id, "m" + strconv.FormatInt(id, 10)})
	}
}

func (db *followDB) queryCount() int { db.mu.Lock(); defer db.mu.Unlock(); return len(db.queries) }

func (db *followDB) queryLog() []string {
	db.mu.Lock()
	defer db.mu.Unlock()
	return append([]string(nil), db.queries...)
}

var (
	followDBs  sync.Map
	followSeq  atomic.Int64
	followOnce sync.Once
	fakeError  = errors.New("table dropped")
)

type followDriver struct{}

func (followDriver) Open(name string) (driver.Conn, error) {
	v, ok := followDBs.Load(name)
	if !ok {
		return nil, fmt.Errorf("no fake db %q", name)
	}
	return &followConn{db: v.(*followDB)}, nil
}

type followConn struct{ db *followDB }

func (c *followConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (c *followConn) Close() error                        { return nil }
func (c *followConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c *followConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	db := c.db
	db.mu.Lock()
	entry := q
	for _, a := range args {
		entry += fmt.Sprintf(" %T:%v", a.Value, a.Value)
	}
	db.queries = append(db.queries, entry)
	fail, block := db.fail, db.block
	all := append([][]driver.Value(nil), db.rows...)
	db.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fail != nil {
		return nil, fail
	}
	var out [][]driver.Value
	switch q {
	case "ALL":
		out = all
	case "INIT":
		out = all[max(len(all)-2, 0):]
	case "NEXT":
		var last int64
		switch v := args[0].Value.(type) {
		case int64:
			last = v
		case string:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, err
			}
			last = n
		default:
			return nil, fmt.Errorf("unexpected key type %T", v)
		}
		for _, r := range all {
			if r[0].(int64) > last {
				out = append(out, r)
			}
		}
	default:
		return nil, fmt.Errorf("unknown query %q", q)
	}
	return &followRows{data: out}, nil
}

type followRows struct {
	data [][]driver.Value
	i    int
}

func (r *followRows) Columns() []string { return []string{"id", "msg"} }
func (r *followRows) Close() error      { return nil }
func (r *followRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

// newFollowFake returns a fake table with rows 1..n and a *sql.DB on it.
func newFollowFake(t *testing.T, n int64) (*followDB, *sql.DB) {
	t.Helper()
	followOnce.Do(func() { sql.Register("dbccfollowfake", followDriver{}) })
	name := fmt.Sprintf("fake%d", followSeq.Add(1))
	db := &followDB{}
	for i := int64(1); i <= n; i++ {
		db.insert(i)
	}
	followDBs.Store(name, db)
	sdb, err := sql.Open("dbccfollowfake", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sdb.Close() })
	return db, sdb
}

var followCols = []ColumnInfo{{Name: "id", Numeric: true}, {Name: "msg"}}

func followResult(ids ...int) *Result {
	rows := make([]Row, len(ids))
	for i, id := range ids {
		rows[i] = Row{strconv.Itoa(id), "m" + strconv.Itoa(id)}
	}
	r := NewResult("logs", followCols, rows)
	r.SQL = "INIT"
	return r
}

func followSpecFor(sdb *sql.DB) FollowSpec {
	return FollowSpec{SQL: "NEXT", Key: "id", Interval: 5 * time.Millisecond,
		Conn: func(context.Context) (Querier, error) { return sdb, nil }}
}

func followIDs(r *Result) string {
	var out []string
	for _, row := range r.Rows() {
		out = append(out, CellText(row[0]))
	}
	return strings.Join(out, ",")
}

func followWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// followExited reports whether the follower goroutine has followExited.
func followExited(r *Result) bool {
	r.mu.Lock()
	f := r.follow
	r.mu.Unlock()
	if f == nil || f.done == nil {
		return true
	}
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

func TestFollowAppendsNewRowsInOrder(t *testing.T) {
	db, sdb := newFollowFake(t, 2)
	r := followResult(1, 2)
	if err := r.SetFollow(followSpecFor(sdb)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	r.OnFollow(func(rows []Row) {
		mu.Lock()
		defer mu.Unlock()
		for _, row := range rows {
			got = append(got, CellText(row[0]))
		}
	}, nil)
	r.StartFollow()
	if !r.Following() || !r.Follows() {
		t.Fatal("not following")
	}
	db.insert(3, 4)
	followWait(t, "rows 3,4", func() bool { return r.Len() == 4 })
	db.insert(5)
	followWait(t, "row 5", func() bool { return r.Len() == 5 })
	if s := followIDs(r); s != "1,2,3,4,5" {
		t.Errorf("rows %s", s)
	}
	mu.Lock()
	if s := strings.Join(got, ","); s != "3,4,5" {
		t.Errorf("appended %s", s)
	}
	mu.Unlock()
	// The first poll binds the loaded key text, later ones the driver's value.
	log := db.queryLog()
	if log[0] != "NEXT string:2" {
		t.Errorf("first poll %q", log[0])
	}
	if !strings.Contains(strings.Join(log, "|"), "NEXT int64:4") {
		t.Errorf("no poll bound the driver's key value: %v", log)
	}
	if st := r.Status(); st != "5 rows · following · 3 new rows · every 0.005 s" {
		t.Errorf("status %q", st)
	}
	// Release stops polling and waits for the goroutine.
	r.Release()
	if !followExited(r) {
		t.Fatal("follower goroutine still running after Release")
	}
	n := db.queryCount()
	db.insert(6)
	time.Sleep(50 * time.Millisecond)
	if db.queryCount() != n {
		t.Errorf("polled after Release: %d -> %d queries", n, db.queryCount())
	}
	if r.Following() {
		t.Error("still following after Release")
	}
}

func TestFollowCapDropsOldest(t *testing.T) {
	_, sdb := newFollowFake(t, 0)
	r := followResult(1, 2)
	sp := followSpecFor(sdb)
	sp.Cap = 3
	if err := r.SetFollow(sp); err != nil {
		t.Fatal(err)
	}
	before := r.Rows()
	if d := r.Append([]Row{{"3", "m3"}}); d != 0 {
		t.Errorf("dropped %d", d)
	}
	if d := r.Append([]Row{{"4", "m4"}, {"5", "m5"}}); d != 2 {
		t.Errorf("dropped %d, want 2", d)
	}
	if s := followIDs(r); s != "3,4,5" || r.Offset() != 2 {
		t.Errorf("rows %s offset %d", s, r.Offset())
	}
	if CellText(before[0][0]) != "1" || CellText(before[1][0]) != "2" {
		t.Error("a slice handed out earlier was modified")
	}
	// More new rows than the cap at once: the newest cap rows remain.
	r.Append([]Row{{"6", ""}, {"7", ""}, {"8", ""}, {"9", ""}})
	if s := followIDs(r); s != "7,8,9" || r.Offset() != 6 {
		t.Errorf("rows %s offset %d", s, r.Offset())
	}
	if st := r.Status(); !strings.HasPrefix(st, "rows 7–9 · following · 0 new rows") {
		t.Errorf("status %q", st)
	}
	// Without paging: one page.
	if r.Page() != 1 || r.HasNextPage() || r.SkipRows(false) != 0 {
		t.Errorf("page %d next %v skip %d", r.Page(), r.HasNextPage(), r.SkipRows(false))
	}
	if err := r.NextPage(context.Background()); !errors.Is(err, ErrFollowing) {
		t.Errorf("NextPage: %v", err)
	}
	if err := r.PrevPage(context.Background()); !errors.Is(err, ErrFollowing) {
		t.Errorf("PrevPage: %v", err)
	}
}

func TestFollowAppendRace(t *testing.T) {
	_, sdb := newFollowFake(t, 0)
	r := followResult()
	sp := followSpecFor(sdb)
	sp.Cap = 50
	if err := r.SetFollow(sp); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				r.Append([]Row{{strconv.Itoa(w*1000 + i), "x"}})
			}
		}(w)
	}
	var readers sync.WaitGroup
	for k := 0; k < 3; k++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rows := r.Rows()
				for _, row := range rows {
					_ = row[0]
				}
				_, _ = r.Len(), r.Offset()
				_ = r.Status()
			}
		}()
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	if r.Len() != 50 || r.Offset() != 950 {
		t.Errorf("len %d offset %d", r.Len(), r.Offset())
	}
}

func TestFollowStopsOnError(t *testing.T) {
	db, sdb := newFollowFake(t, 2)
	r := followResult(1, 2)
	r.SetFollow(followSpecFor(sdb))
	ended := make(chan error, 1)
	r.OnFollow(nil, func(err error) { ended <- err })
	db.mu.Lock()
	db.fail = fakeError
	db.mu.Unlock()
	r.StartFollow()
	select {
	case err := <-ended:
		if !errors.Is(err, fakeError) {
			t.Errorf("end: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("following did not end")
	}
	followWait(t, "goroutine exit", func() bool { return followExited(r) })
	if r.Following() || !r.Follows() {
		t.Error("following state after error")
	}
	if _, _, active, err := r.FollowState(); active || !errors.Is(err, fakeError) {
		t.Errorf("state %v %v", active, err)
	}
	if st := r.Status(); !strings.Contains(st, "following stopped") {
		t.Errorf("status %q", st)
	}
	// Two attempts (one retry), then no more.
	if n := db.queryCount(); n != 2 {
		t.Errorf("%d polls, want 2", n)
	}
	r.Release()
}

func TestFollowEndsSilentlyWhenSessionCloses(t *testing.T) {
	_, sdb := newFollowFake(t, 1)
	r := followResult(1)
	gone := make(chan struct{})
	sp := followSpecFor(sdb)
	sp.Gone = gone
	r.SetFollow(sp)
	ended := make(chan error, 1)
	r.OnFollow(nil, func(err error) { ended <- err })
	r.StartFollow()
	close(gone)
	select {
	case err := <-ended:
		if err != nil {
			t.Errorf("end: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("following did not end")
	}
	followWait(t, "goroutine exit", func() bool { return followExited(r) })
}

func TestFollowEmptyResultPollsItsStatement(t *testing.T) {
	db, sdb := newFollowFake(t, 0)
	r := followResult()
	r.SetFollow(followSpecFor(sdb))
	r.StartFollow()
	defer r.Release()
	followWait(t, "a poll", func() bool { return db.queryCount() > 0 })
	db.insert(1, 2, 3)
	followWait(t, "last rows", func() bool { return r.Len() == 2 })
	db.insert(4)
	followWait(t, "row 4", func() bool { return r.Len() == 3 })
	if s := followIDs(r); s != "2,3,4" {
		t.Errorf("rows %s", s)
	}
	if log := db.queryLog(); log[0] != "INIT" || !strings.HasPrefix(log[len(log)-1], "NEXT int64:") {
		t.Errorf("queries %v", log)
	}
}

func TestFollowReleaseWhilePollRuns(t *testing.T) {
	db, sdb := newFollowFake(t, 1)
	db.block = true
	r := followResult(1)
	r.SetFollow(followSpecFor(sdb))
	r.StartFollow()
	followWait(t, "a blocked poll", func() bool { return db.queryCount() > 0 })
	done := make(chan struct{})
	go func() { r.Release(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Release did not return while a poll was running")
	}
	if !followExited(r) {
		t.Error("goroutine still running")
	}
}

func TestSetFollowDrainsCursorKeepingNewest(t *testing.T) {
	_, sdb := newFollowFake(t, 7)
	rows, err := sdb.QueryContext(context.Background(), "ALL")
	if err != nil {
		t.Fatal(err)
	}
	cols, err := columnInfos(rows)
	if err != nil {
		t.Fatal(err)
	}
	r := &Result{Title: "t", SQL: "ALL", Columns: cols, PageSize: 3}
	r.cursor = &Cursor{rows: rows, cols: cols}
	if _, err := r.Fetch(2); err != nil {
		t.Fatal(err)
	}
	if err := r.SetFollow(FollowSpec{SQL: "NEXT", Key: "ID", Conn: func(context.Context) (Querier, error) { return sdb, nil }}); err != nil {
		t.Fatal(err)
	}
	if s := followIDs(r); s != "5,6,7" || r.Offset() != 4 || r.More() || r.Cursor() != nil {
		t.Errorf("rows %s offset %d more %v", s, r.Offset(), r.More())
	}
	r.mu.Lock()
	last, interval := r.follow.lastText, r.follow.spec.Interval
	r.mu.Unlock()
	if last != "7" || interval != DefaultFollowInterval {
		t.Errorf("last key %q interval %v", last, interval)
	}
	if err := followResult(1).SetFollow(FollowSpec{SQL: "NEXT", Key: "nope", Conn: followSpecFor(sdb).Conn}); !errors.Is(err, ErrFollowKey) {
		t.Errorf("unknown key: %v", err)
	}
}
