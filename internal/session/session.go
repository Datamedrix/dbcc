// Package session manages pinned connections, the auxiliary connection,
// transactions, cancel and the open-cursor rule (spec 6.3).
package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/meta"
)

// Session is one open profile: a pinned main connection for all user
// statements plus an auxiliary connection for metadata and cancel.
type Session struct {
	Profile *config.Profile
	Dialect dialect.Dialect

	cred config.Credentials

	mu         sync.Mutex
	mainDB     *sql.DB
	main       *sql.Conn
	auxDB      *sql.DB
	id         string // server-side id of the main connection
	database   string
	schema     string
	cursor     io.Closer // open lazy cursor holding the main session
	txnOpen    bool
	auxByDB    map[string]*sql.DB // aux pools for other databases (PostgreSQL)
	stmtCancel context.CancelFunc
	done       chan struct{} // closed by Close (see Done)
}

// Done returns a channel that is closed when the session is closed.
// Background work on its connections (following results, spec A26) stops
// on it.
func (s *Session) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done == nil {
		s.done = make(chan struct{})
	}
	return s.done
}

// Open connects a profile. database overrides the profile's database if set.
func Open(ctx context.Context, p *config.Profile, cred config.Credentials, database string) (*Session, error) {
	d, err := dialect.For(p.Driver)
	if err != nil {
		return nil, err
	}
	if database == "" {
		database = p.Database
	}
	s := &Session{Profile: p, Dialect: d, cred: cred}
	if err := s.connect(ctx, database); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) connect(ctx context.Context, database string) error {
	mainDB, err := s.Dialect.Open(ctx, s.Profile, s.cred, database)
	if err != nil {
		return err
	}
	mainDB.SetMaxOpenConns(1)
	mainDB.SetMaxIdleConns(1)
	main, err := mainDB.Conn(ctx)
	if err != nil {
		mainDB.Close()
		return err
	}
	if refined, err := s.Dialect.Detect(ctx, main); err == nil && refined != nil {
		s.Dialect = refined
	} else if err != nil {
		main.Close()
		mainDB.Close()
		return err
	}
	auxDB, err := s.Dialect.Open(ctx, s.Profile, s.cred, database)
	if err != nil {
		main.Close()
		mainDB.Close()
		return err
	}
	auxDB.SetMaxOpenConns(2)
	id, err := s.Dialect.SessionID(ctx, main)
	if err != nil {
		id = ""
	}
	if s.Profile.ReadOnly {
		if err := s.Dialect.SetReadOnly(ctx, main); err != nil {
			main.Close()
			mainDB.Close()
			auxDB.Close()
			return fmt.Errorf("set read-only: %w", err)
		}
	}
	if !s.Profile.IsAutocommit() {
		if err := s.Dialect.SetAutocommit(ctx, main, false); err != nil {
			main.Close()
			mainDB.Close()
			auxDB.Close()
			return fmt.Errorf("disable autocommit: %w", err)
		}
	}
	cur, _ := s.Dialect.CurrentDatabase(ctx, main)
	if cur == "" {
		cur = database
	}
	schema, _ := s.Dialect.CurrentSchema(ctx, main)
	s.mu.Lock()
	s.mainDB, s.main, s.auxDB, s.id, s.database, s.schema = mainDB, main, auxDB, id, cur, schema
	s.mu.Unlock()
	return nil
}

// Main returns the pinned main connection. Callers must close any open
// cursor first (CloseCursor) — the runner does this.
func (s *Session) Main() *sql.Conn { s.mu.Lock(); defer s.mu.Unlock(); return s.main }

// Aux returns the auxiliary connection pool for metadata queries.
func (s *Session) Aux() dialect.Conn { s.mu.Lock(); defer s.mu.Unlock(); return s.auxDB }

// AuxFor returns an auxiliary connection for metadata of database db. On
// dialects that can query any database from one connection this is Aux();
// on PostgreSQL it opens (once) a pool connected to db, so the tree can
// expand another database in the background (spec 7.2).
func (s *Session) AuxFor(ctx context.Context, db string) (dialect.Conn, error) {
	s.mu.Lock()
	aux, cur := s.auxDB, s.database
	s.mu.Unlock()
	if db == "" || db == cur || !s.Dialect.HasSchemas() {
		return aux, nil
	}
	s.mu.Lock()
	if p, ok := s.auxByDB[db]; ok {
		s.mu.Unlock()
		return p, nil
	}
	s.mu.Unlock()
	p, err := s.Dialect.Open(ctx, s.Profile, s.cred, db)
	if err != nil {
		return nil, err
	}
	p.SetMaxOpenConns(2)
	if err := p.PingContext(ctx); err != nil {
		p.Close()
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auxByDB == nil {
		s.auxByDB = map[string]*sql.DB{}
	}
	if old, ok := s.auxByDB[db]; ok {
		p.Close()
		return old, nil
	}
	s.auxByDB[db] = p
	return p, nil
}

// StatementContext derives the context for one statement on the main
// session; Cancel cancels it on dialects that cancel via the driver.
// The caller must call done when the statement has finished; with
// keepOpen (a lazy cursor still reads from the context) the context is not
// cancelled — database/sql would close the rows.
func (s *Session) StatementContext(parent context.Context) (ctx context.Context, done func(keepOpen bool)) {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.stmtCancel = cancel
	s.mu.Unlock()
	return ctx, func(keepOpen bool) {
		s.mu.Lock()
		s.stmtCancel = nil
		s.mu.Unlock()
		if !keepOpen {
			cancel()
		}
	}
}

// AbortStreaming stops a statement whose result the server is still
// sending: KILL QUERY on MySQL/MariaDB, or cancel (the statement context
// of the stream) on dialects that cancel via the driver. The connection
// and its transaction stay intact.
func (s *Session) AbortStreaming(cancel context.CancelFunc) {
	s.mu.Lock()
	id, aux := s.id, s.auxDB
	s.mu.Unlock()
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	err := s.Dialect.Cancel(ctx, aux, id)
	if errors.Is(err, dialect.ErrCancelViaContext) && cancel != nil {
		cancel()
	}
}

// Database returns the current database of the main session.
func (s *Session) Database() string { s.mu.Lock(); defer s.mu.Unlock(); return s.database }

// Schema returns the current schema (dialects with a schema level).
func (s *Session) Schema() string { s.mu.Lock(); defer s.mu.Unlock(); return s.schema }

// Path returns the current navigation path of the session.
func (s *Session) Path() meta.Path {
	return meta.Path{Conn: s.Profile.Name, Database: s.Database(), Schema: s.Schema()}
}

// ChangeSchema sets the default schema of the main session.
func (s *Session) ChangeSchema(ctx context.Context, schema string) error {
	s.CloseCursor()
	if err := s.Dialect.ChangeSchema(ctx, s.Main(), schema); err != nil {
		return err
	}
	s.mu.Lock()
	s.schema = schema
	s.mu.Unlock()
	return nil
}

// TxnOpen reports whether a manual transaction is open.
func (s *Session) TxnOpen() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.txnOpen }

// SetTxnOpen records the transaction state (manual-transaction profiles).
func (s *Session) SetTxnOpen(v bool) { s.mu.Lock(); s.txnOpen = v; s.mu.Unlock() }

// SetCursor registers a lazily loaded result that holds the main session.
func (s *Session) SetCursor(c io.Closer) {
	s.mu.Lock()
	old := s.cursor
	s.cursor = c
	s.mu.Unlock()
	if old != nil && old != c {
		old.Close()
	}
}

// CloseCursor closes the open cursor, if any (open-cursor rule, spec 6.3).
func (s *Session) CloseCursor() {
	s.mu.Lock()
	c := s.cursor
	s.cursor = nil
	s.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// ReleaseCursor forgets c if it is the registered cursor (it closed itself).
func (s *Session) ReleaseCursor(c io.Closer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursor == c {
		s.cursor = nil
	}
}

// ChangeDatabase issues USE or reconnects, as the dialect requires.
func (s *Session) ChangeDatabase(ctx context.Context, db string) error {
	s.CloseCursor()
	reconnect, err := s.Dialect.ChangeDatabase(ctx, s.Main(), db)
	if err != nil {
		return err
	}
	if reconnect {
		old := s.snapshot()
		if err := s.connect(ctx, db); err != nil {
			return err
		}
		old.close()
		// The new connection has no transaction (the workspace asked
		// about an open one before the change).
		s.SetTxnOpen(false)
		return nil
	}
	s.mu.Lock()
	s.database = db
	s.mu.Unlock()
	return nil
}

// Cancel cancels the statement running on the main session.
func (s *Session) Cancel(ctx context.Context) error {
	s.mu.Lock()
	id, aux, cancel := s.id, s.auxDB, s.stmtCancel
	s.mu.Unlock()
	err := s.Dialect.Cancel(ctx, aux, id)
	if errors.Is(err, dialect.ErrCancelViaContext) {
		if cancel != nil {
			cancel()
		}
		return nil
	}
	return err
}

type conns struct {
	mainDB, auxDB *sql.DB
	main          *sql.Conn
	extra         []*sql.DB
}

func (s *Session) snapshot() conns {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := conns{mainDB: s.mainDB, auxDB: s.auxDB, main: s.main}
	for _, p := range s.auxByDB {
		c.extra = append(c.extra, p)
	}
	s.auxByDB = nil
	return c
}

func (c conns) close() {
	for _, p := range c.extra {
		p.Close()
	}
	if c.main != nil {
		c.main.Close()
	}
	if c.mainDB != nil {
		c.mainDB.Close()
	}
	if c.auxDB != nil {
		c.auxDB.Close()
	}
}

// Close closes cursor and all connections.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.done == nil {
		s.done = make(chan struct{})
	}
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Unlock()
	s.CloseCursor()
	s.snapshot().close()
	return nil
}
