// Package dialect defines the Dialect interface. All dialect differences the
// engine needs live behind it; no "if dialect == …" outside this tree (spec 6).
package dialect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"dbcc/internal/config"
	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

// Conn is satisfied by *sql.Conn, *sql.DB and *sql.Tx.
type Conn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ObjectKinds selects which kinds ListObjects returns; nil means all.
type ObjectKinds []meta.ObjectKind

// Has reports whether k is selected.
func (ks ObjectKinds) Has(k meta.ObjectKind) bool {
	if len(ks) == 0 {
		return true
	}
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

// FieldError is a server error mapped back to a column or constraint.
type FieldError struct {
	Column     string // empty if not derivable
	Constraint string
	Message    string
	Err        error
}

func (e *FieldError) Error() string { return e.Message }
func (e *FieldError) Unwrap() error { return e.Err }

// Dialect is the per-database-family strategy (spec 6.1).
type Dialect interface {
	Name() string // "mysql", "mariadb", "postgres", "mssql"
	Open(ctx context.Context, p *config.Profile, cred config.Credentials, database string) (*sql.DB, error)
	// Detect may refine the dialect after connecting (MySQL → MariaDB).
	Detect(ctx context.Context, c Conn) (Dialect, error)

	// Navigation
	HasSchemas() bool
	ListDatabases(ctx context.Context, c Conn) ([]meta.Object, error)
	ListSchemas(ctx context.Context, c Conn, db string) ([]meta.Object, error)
	ListObjects(ctx context.Context, c Conn, at meta.Path, kinds ObjectKinds) ([]meta.Object, error)
	// ChangeDatabase switches the database of the session connection c.
	// It returns reconnect=true when the dialect cannot switch in place
	// (PostgreSQL); the session then reopens its connection on db.
	ChangeDatabase(ctx context.Context, c Conn, db string) (reconnect bool, err error)
	CurrentDatabase(ctx context.Context, c Conn) (string, error)
	// ChangeSchema sets the default schema (search_path on PostgreSQL);
	// CurrentSchema reports it. Dialects without schemas return "" / nil.
	ChangeSchema(ctx context.Context, c Conn, schema string) error
	CurrentSchema(ctx context.Context, c Conn) (string, error)

	// Metadata
	DescribeTable(ctx context.Context, c Conn, obj meta.Path) (*meta.Table, error)
	ObjectSource(ctx context.Context, c Conn, obj meta.Path, kind meta.ObjectKind) (string, error)

	// SQL generation
	QuoteIdent(name string) string
	Qualify(obj meta.Path) string
	LimitQuery(query string, n int) string
	CopyTable(src, dst meta.Path, t *meta.Table, withStructure bool) ([]string, error)
	ReplaceScript(ctx context.Context, c Conn, obj meta.Path, kind meta.ObjectKind) (string, error)
	// TextSearchPredicate returns the quick-search condition over columns;
	// param is the placeholder of the search text (Placeholder(n) for the
	// next free argument number n).
	TextSearchPredicate(columns []meta.Column, param string) string
	// TextSearchParamCount is how many times the caller binds the search
	// text for TextSearchPredicate: once per searchable column for "?"
	// placeholders (MySQL), 1 for numbered ones that can repeat ($1,
	// @p1), 0 when no column is searchable.
	TextSearchParamCount(columns []meta.Column) int
	// Placeholder returns the bind placeholder of argument n (1-based) as
	// the driver expects it: "?" (MySQL), "$n" (PostgreSQL), "@pn" (MSSQL).
	Placeholder(n int) string
	// InsertDefaults is an INSERT that supplies no column at all, so every
	// column takes its default ("INSERT INTO t () VALUES ()" on MySQL,
	// "INSERT INTO t DEFAULT VALUES" elsewhere).
	InsertDefaults(qualifiedTable string) string

	// Text handling
	Splitter() sqltext.Splitter
	Tokenizer() sqltext.Tokenizer
	LexerName() string
	Keywords() []string
	Functions() []string

	// Session control
	SessionID(ctx context.Context, c Conn) (string, error)
	Cancel(ctx context.Context, aux Conn, sessionID string) error
	SetReadOnly(ctx context.Context, c Conn) error
	SetAutocommit(ctx context.Context, c Conn, on bool) error
	ImplicitCommit(stmt string) bool
	// WarningCount and Warnings report server warnings of the last
	// statement on c (MySQL @@warning_count / SHOW WARNINGS); dialects
	// without the concept return 0 / nil.
	WarningCount(ctx context.Context, c Conn) (int, error)
	Warnings(ctx context.Context, c Conn) ([]string, error)

	// Statement classification (token-level, no parser)
	ReturnsRows(stmt string) bool // run with Query rather than Exec
	// MultiResult reports statements that may return several result sets
	// (MySQL CALL, MSSQL batches with EXEC or more than one top-level
	// SELECT). The runner buffers every set of such a statement in its
	// own tab instead of keeping only the first one lazily open.
	MultiResult(stmt string) bool
	IsRead(stmt string) bool // read-only guard and confirm_writes (spec 12)

	// Errors
	MapError(err error, t *meta.Table) *FieldError
}

// TxnBeginner is implemented by dialects that need an explicit statement
// to open a manual transaction (PostgreSQL has no session autocommit
// switch). BeginFor returns the statement the runner sends before stmt
// when the profile is not autocommit and no transaction is open, or ""
// when stmt must not be preceded by one (transaction control, session
// settings, statements that cannot run inside a transaction block).
type TxnBeginner interface {
	BeginFor(stmt string) string
}

// TxnTracker is implemented by dialects that can ask the server whether a
// transaction is open on the session connection c (PostgreSQL
// transaction status, MSSQL @@TRANCOUNT). The runner uses it after each
// statement of a manual-transaction profile, so batches such as
// "INSERT …; COMMIT" are tracked correctly; dialects without it fall back
// to a keyword heuristic. c has no open result set when TxnOpen is called.
type TxnTracker interface {
	TxnOpen(ctx context.Context, c Conn) (bool, error)
}

// Rebind rewrites the "?" placeholders of q into the dialect's own
// (Placeholder(1), Placeholder(2), …) in order. Strings, quoted
// identifiers and comments are left alone, because the dialect's tokenizer
// decides what a "?" is. On dialects whose placeholder is "?" q is
// returned unchanged. Callers rebind only statements that are executed
// with arguments: on PostgreSQL a lone "?" is also the jsonb key-exists
// operator. Native placeholders ($1, @p1) are left as they are; mixing
// them with "?" in one statement is not supported.
func Rebind(d Dialect, q string) string {
	if d.Placeholder(1) == "?" || !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	n := 0
	for _, t := range d.Tokenizer().Tokenize(q) {
		if t.Text == "?" && (t.Kind == sqltext.TokParam || t.Kind == sqltext.TokPunct) {
			n++
			b.WriteString(d.Placeholder(n))
			continue
		}
		b.WriteString(t.Text)
	}
	return b.String()
}

var (
	mu       sync.RWMutex
	registry = map[string]Dialect{}
)

// Register makes a dialect available under its driver name.
func Register(driver string, d Dialect) {
	mu.Lock()
	defer mu.Unlock()
	registry[driver] = d
}

// ReplaceWarner is implemented by dialects whose replace scripts have side
// effects the owner must know about before running them (spec 9.1, A21).
type ReplaceWarner interface {
	ReplaceWarning(kind meta.ObjectKind) string
}

// ErrCancelViaContext is returned by Dialect.Cancel when the dialect
// cancels through the driver: the session then cancels the context of the
// running statement (PostgreSQL, MSSQL; spec 6.3).
var ErrCancelViaContext = errors.New("cancel via context")

// ErrUnknownDriver is returned by For for an unregistered driver.
var ErrUnknownDriver = errors.New("unknown driver")

// For returns the dialect registered for a profile driver.
func For(driver string) (Dialect, error) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := registry[driver]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownDriver, driver)
	}
	return d, nil
}
