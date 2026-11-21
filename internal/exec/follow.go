package exec

// Following (tail -f, spec 8, A26): after its initial rows, a result polls
// for rows whose key is greater than the last key it has seen and appends
// them at its end. Polling runs on its own goroutine on the auxiliary
// connection of the object's session — never on the main session (the
// open-cursor rule, spec 6.3) and never on the caller's goroutine.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultFollowCap is the number of rows a following result keeps when it
// has no page size (headless mode): older rows are dropped from the front.
const DefaultFollowCap = 3000

// Follow intervals.
const (
	DefaultFollowInterval = 2 * time.Second
	MinFollowInterval     = 500 * time.Millisecond
)

// Following errors.
var (
	// ErrFollowing is returned by the paging methods of a following result:
	// it is one growing page.
	ErrFollowing = errors.New("a following result has no pages: new rows are appended to this one (tail -f)")
	// ErrFollowKey is returned by SetFollow when the key column is not in
	// the result.
	ErrFollowKey = errors.New("follow: key column not in the result")
)

// Querier runs a query; *sql.DB, *sql.Conn and dialect.Conn implement it.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// FollowSpec configures a following result (Result.SetFollow).
type FollowSpec struct {
	// SQL selects the rows after the last seen key, ordered by the key
	// ascending; it has exactly one bind parameter (in the dialect's
	// placeholder syntax) for that key.
	SQL string
	// Key is the name of the key column in the result.
	Key string
	// Interval between polls (default DefaultFollowInterval; ctx:grid
	// accepts at least MinFollowInterval).
	Interval time.Duration
	// Cap is the most rows the result keeps; older rows are dropped from
	// the front. 0 means the result's PageSize, or DefaultFollowCap.
	Cap int
	// Conn returns the connection to poll on (the session's auxiliary
	// connection for the object's database). It is called for every poll,
	// so a reconnect of the session is picked up.
	Conn func(ctx context.Context) (Querier, error)
	// Gone is closed when the connection's session closes: following then
	// ends silently. May be nil.
	Gone <-chan struct{}
}

// follower is the state of a following result. Fields after the mutex
// line of Result are guarded by Result.mu unless noted.
type follower struct {
	spec    FollowSpec
	key     int // index of the key column
	added   int // rows appended by polling
	active  bool
	err     error // why following ended (nil: stopped or the session closed)
	started bool
	cancel  context.CancelFunc
	done    chan struct{} // closed when the goroutine has exited

	onAppend func(rows []Row)
	onEnd    func(err error)

	// Owned by the goroutine (and by SetFollow before it starts).
	last     any    // bind value of the last key (nil: no row seen yet)
	lastText string // its text form, to drop a row seen twice
	initSQL  string // the result's statement, polled while no row was seen
	initArgs []any
}

// SetFollow makes r a following result. It reads the rest of the cursor
// (keeping the newest Cap rows), so the main session is free afterwards,
// and records the last key of the loaded rows. Paging no longer applies.
// Polling starts with StartFollow, after the host has installed its
// callbacks (OnFollow). While no row has been seen (empty initial
// result) the follower polls the result's own statement instead of
// spec.SQL.
func (r *Result) SetFollow(spec FollowSpec) error {
	if spec.SQL == "" || spec.Conn == nil {
		return errors.New("follow: SQL and connection are required")
	}
	key := -1
	for i, c := range r.Columns {
		if c.Name == spec.Key {
			key = i
			break
		}
	}
	if key < 0 {
		for i, c := range r.Columns {
			if strings.EqualFold(c.Name, spec.Key) {
				key = i
				break
			}
		}
	}
	if key < 0 {
		return fmt.Errorf("%w: %q", ErrFollowKey, spec.Key)
	}
	if spec.Interval <= 0 {
		spec.Interval = DefaultFollowInterval
	}
	r.mu.Lock()
	if spec.Cap <= 0 {
		spec.Cap = r.PageSize
	}
	if spec.Cap <= 0 {
		spec.Cap = DefaultFollowCap
	}
	f := &follower{spec: spec, key: key, active: true, initSQL: r.SQL, initArgs: r.Args}
	r.follow = f
	r.MaxRows = 0
	c, ahead := r.cursor, r.ahead
	r.ahead = nil
	r.mu.Unlock()
	if ahead != nil {
		r.Append([]Row{ahead})
	}
	if c != nil {
		for !c.Done() {
			rows, err := c.Fetch(500)
			r.Append(rows)
			if err != nil {
				c.Close()
				r.mu.Lock()
				r.follow = nil
				r.mu.Unlock()
				return err
			}
		}
		c.Close()
	}
	r.mu.Lock()
	r.cursor = nil
	r.Partial, r.Capped, r.noNext = false, false, true
	if n := len(r.rows); n > 0 {
		v := r.rows[n-1][key]
		f.last, f.lastText = v, CellText(v)
	}
	r.mu.Unlock()
	return nil
}

// OnFollow installs the callbacks of a following result: onAppend gets
// the rows each poll appended, onEnd is called once when following ends
// on its own (err: why; nil when the session closed). Both run on the
// follower's goroutine and must not block on the UI goroutine (which may
// be waiting in Release); either may be nil.
func (r *Result) OnFollow(onAppend func(rows []Row), onEnd func(err error)) {
	r.mu.Lock()
	if f := r.follow; f != nil {
		f.onAppend, f.onEnd = onAppend, onEnd
	}
	r.mu.Unlock()
}

// StartFollow starts polling (once; after SetFollow).
func (r *Result) StartFollow() {
	r.mu.Lock()
	f := r.follow
	if f == nil || f.started || !f.active || r.released {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.started, f.cancel, f.done = true, cancel, make(chan struct{})
	r.mu.Unlock()
	go r.followLoop(ctx, f)
}

// StopFollow stops polling and waits until the follower goroutine has
// exited; idempotent. The rows stay.
func (r *Result) StopFollow() {
	r.mu.Lock()
	f := r.follow
	if f == nil {
		r.mu.Unlock()
		return
	}
	f.active = false
	cancel, done := f.cancel, f.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Follows reports whether r was made a following result (also after
// following stopped: it stays one page without paging).
func (r *Result) Follows() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.follow != nil }

// Following reports whether r is still following.
func (r *Result) Following() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.follow != nil && r.follow.active
}

// FollowState returns the rows appended by polling, the interval, whether
// polling is active and why it ended.
func (r *Result) FollowState() (added int, interval time.Duration, active bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f := r.follow; f != nil {
		return f.added, f.spec.Interval, f.active, f.err
	}
	return 0, 0, false, nil
}

// Append adds rows at the end of the result, thread-safe. A following
// result keeps at most its cap rows: the oldest are dropped from the
// front and Offset grows by their number (row numbers stay those of the
// rows seen since the start). It returns how many rows were dropped.
func (r *Result) Append(rows []Row) int {
	if len(rows) == 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return 0
	}
	limit := 0
	if r.follow != nil {
		limit = r.follow.spec.Cap
	}
	// A new backing array when dropping: Rows() hands out the old slice,
	// which must stay valid for readers on other goroutines.
	if limit > 0 && len(r.rows)+len(rows) > limit {
		all := len(r.rows) + len(rows)
		drop := all - limit
		next := make([]Row, 0, limit)
		if drop < len(r.rows) {
			next = append(next, r.rows[drop:]...)
			next = append(next, rows...)
		} else {
			next = append(next, rows[drop-len(r.rows):]...)
		}
		r.rows = next
		r.offset += drop
		return drop
	}
	r.rows = append(r.rows, rows...)
	return 0
}

// followLoop polls until it is stopped, the session closes or a poll fails.
func (r *Result) followLoop(ctx context.Context, f *follower) {
	defer close(f.done)
	timer := time.NewTimer(f.spec.Interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.spec.Gone:
			r.endFollow(f, nil)
			return
		case <-timer.C:
		}
		rows, full, err := r.poll(ctx, f)
		if err != nil && ctx.Err() == nil && !gone(f.spec.Gone) {
			// One retry with a fresh connection (a reconnect of the
			// session closes the pool a poll was using).
			rows, full, err = r.poll(ctx, f)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if gone(f.spec.Gone) {
				err = nil
			}
			r.endFollow(f, err)
			return
		}
		if len(rows) > 0 {
			r.Append(rows)
			r.mu.Lock()
			f.added += len(rows)
			cb := f.onAppend
			r.mu.Unlock()
			if cb != nil {
				cb(rows)
			}
		}
		if full {
			timer.Reset(0) // more rows are waiting
		} else {
			timer.Reset(f.spec.Interval)
		}
	}
}

func gone(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (r *Result) endFollow(f *follower, err error) {
	r.mu.Lock()
	f.active = false
	f.err = err
	cb := f.onEnd
	r.mu.Unlock()
	if cb != nil {
		cb(err)
	}
}

// poll reads the rows after the last key (at most the cap; full reports
// that the cap was reached). It advances the last key.
func (r *Result) poll(ctx context.Context, f *follower) (out []Row, full bool, err error) {
	conn, err := f.spec.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	q, args := f.spec.SQL, []any{f.last}
	if f.last == nil {
		q, args = f.initSQL, f.initArgs
	}
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	cols, err := columnInfos(rows)
	if err != nil {
		return nil, false, err
	}
	if len(cols) != len(r.Columns) {
		return nil, false, ErrColumnsChanged
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for len(out) < f.spec.Cap && rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return out, false, err
		}
		row := make(Row, len(cols))
		for i, v := range vals {
			row[i] = normalize(v, cols[i])
		}
		text := CellText(row[f.key])
		if f.last != nil && text == f.lastText {
			continue // seen already (a key rendered with less precision than the server's)
		}
		// Bind the driver's own key value next time (full precision), except
		// bytes, which drivers would send as binary.
		raw := vals[f.key]
		if _, isBytes := raw.([]byte); isBytes || raw == nil {
			raw = row[f.key]
		}
		f.last, f.lastText = raw, text
		out = append(out, row)
	}
	full = len(out) >= f.spec.Cap
	if err := rows.Err(); err != nil {
		return out, false, err
	}
	return out, full, nil
}
