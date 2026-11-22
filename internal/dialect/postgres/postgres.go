// Package postgres implements the PostgreSQL dialect (spec M9) on top of
// github.com/jackc/pgx/v5 through its database/sql adapter (pgx/v5/stdlib).
//
// Contracts that differ from MySQL and that callers must know:
//
//   - Bound parameters use $1, $2, … (pgx does not understand "?"). Rebind
//     turns "?" placeholders into $n; Placeholder(n) returns "$n".
//   - TextSearchPredicate uses ONE parameter for all columns: bind the
//     search text once (TextSearchParamCount).
//   - A connection is bound to one database. ChangeDatabase reports
//     reconnect=true; metadata of another database needs a connection to
//     that database (session.AuxFor). Metadata methods check that the
//     connection's current database matches the requested one.
//   - Cancel returns dialect.ErrCancelViaContext: the session cancels the
//     statement context, and pgx is configured to send a cancel request to
//     the server, which leaves the pinned connection usable.
//   - There is no session-level autocommit switch: SetAutocommit is a no-op
//     and a manual-transaction profile needs BEGIN before the first
//     statement of each transaction (ExplicitBegin, NoTransactionBlock).
//   - DDL is transactional: ImplicitCommit is always false.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/stdlib"

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

func init() { dialect.Register("postgres", &Postgres{}) }

// Postgres is the PostgreSQL dialect. Detect fills in the server version.
type Postgres struct {
	Version    string // server_version, e.g. "17.2 (Debian 17.2-1.pgdg120+1)"
	VersionNum int    // server_version_num, e.g. 170002; 0 = unknown (treated as new)
}

func (p *Postgres) Name() string { return "postgres" }

// atLeast reports whether the server is at least the given major version.
func (p *Postgres) atLeast(major int) bool { return p.VersionNum == 0 || p.VersionNum >= major*10000 }

// Open returns a pool for the profile on database (default "postgres").
//
// Connection settings: cred.Host/Port (default 127.0.0.1:5432) or
// cred.Socket, which is the socket directory (e.g. /var/run/postgresql) or
// the socket file itself (…/.s.PGSQL.5432). p.Params are passed as libpq
// keyword/value settings (sslmode, sslrootcert, connect_timeout,
// application_name, options, search_path, …); unknown keys become run-time
// parameters, as in pgx.ParseConfig. Like libpq, pgx also honours PG*
// environment variables for settings that are not given.
//
// The connection is configured for an interactive client:
//   - query exec mode "describe_exec" (no client-side statement cache, so
//     DDL never invalidates cached plans) unless default_query_exec_mode is
//     set in Params;
//   - cancelling a statement context sends a cancel request to the server
//     instead of breaking the connection (see Cancel);
//   - NOTICE and WARNING messages are collected for Warnings.
func (p *Postgres) Open(ctx context.Context, prof *config.Profile, cred config.Credentials, database string) (*sql.DB, error) {
	cfg, err := p.connConfig(prof, cred, database)
	if err != nil {
		return nil, err
	}
	return stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(_ context.Context, c *pgx.Conn) error {
		c.PgConn().CustomData()[noticeKey] = &notices{}
		return nil
	})), nil
}

func (p *Postgres) connConfig(prof *config.Profile, cred config.Credentials, database string) (*pgx.ConnConfig, error) {
	kv := map[string]string{}
	if cred.Socket != "" {
		dir, port := cred.Socket, ""
		if base := filepath.Base(dir); strings.HasPrefix(base, ".s.PGSQL.") {
			dir, port = filepath.Dir(dir), strings.TrimPrefix(base, ".s.PGSQL.")
		}
		kv["host"] = dir
		if port != "" {
			kv["port"] = port
		}
	} else {
		kv["host"] = cred.Host
		if kv["host"] == "" {
			kv["host"] = "127.0.0.1"
		}
	}
	if _, ok := kv["port"]; !ok {
		kv["port"] = "5432"
		if cred.Port != 0 {
			kv["port"] = strconv.Itoa(cred.Port)
		}
	}
	if cred.User != "" {
		kv["user"] = cred.User
	}
	if cred.Password != "" {
		kv["password"] = cred.Password
	}
	if database == "" {
		database = "postgres"
	}
	kv["dbname"] = database
	kv["application_name"] = "dbcc"
	if prof != nil {
		for k, v := range prof.Params {
			kv[k] = v
		}
	}
	if _, ok := kv["connect_timeout"]; !ok {
		kv["connect_timeout"] = "10"
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var dsn strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&dsn, "%s='%s' ", k, strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(kv[k]))
	}
	cfg, err := pgx.ParseConfig(dsn.String())
	if err != nil {
		return nil, err
	}
	if cred.Dial != nil {
		// SSH tunnel (A27): dial through it, cancel requests included; the
		// host name is resolved on the far side, not locally.
		cfg.DialFunc = pgconn.DialFunc(cred.Dial)
		cfg.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }
	}
	if _, ok := kv["default_query_exec_mode"]; !ok {
		cfg.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	}
	cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		// Send a cancel request at once; break the connection only if the
		// server has not answered 30 s later.
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: 30 * time.Second}
	}
	cfg.OnNotice = func(c *pgconn.PgConn, n *pgconn.Notice) {
		if b, ok := c.CustomData()[noticeKey].(*notices); ok {
			b.add(n)
		}
	}
	return cfg, nil
}

func (p *Postgres) Detect(ctx context.Context, c dialect.Conn) (dialect.Dialect, error) {
	var v string
	var num int
	err := c.QueryRowContext(ctx, "SELECT current_setting('server_version'), current_setting('server_version_num')::int").Scan(&v, &num)
	if err != nil {
		return nil, err
	}
	return &Postgres{Version: v, VersionNum: num}, nil
}

func (p *Postgres) HasSchemas() bool { return true }

// ListDatabases lists the databases that accept connections (datallowconn),
// user databases first, then the template databases marked System.
// template0 does not accept connections and is not listed; "postgres" is an
// ordinary database.
func (p *Postgres) ListDatabases(ctx context.Context, c dialect.Conn) ([]meta.Object, error) {
	rows, err := c.QueryContext(ctx, `SELECT datname, datistemplate, COALESCE(shobj_description(oid, 'pg_database'), '')
		FROM pg_database WHERE datallowconn ORDER BY datistemplate, datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []meta.Object
	for rows.Next() {
		var o meta.Object
		if err := rows.Scan(&o.Name, &o.System, &o.Comment); err != nil {
			return nil, err
		}
		o.Kind = meta.KindDatabase
		out = append(out, o)
	}
	return out, rows.Err()
}

// checkDatabase makes sure c is connected to db (if db is given) and
// returns the connection's database.
func (p *Postgres) checkDatabase(ctx context.Context, c dialect.Conn, db string) (string, error) {
	cur, err := p.CurrentDatabase(ctx, c)
	if err != nil {
		return "", err
	}
	if db != "" && db != cur {
		return "", fmt.Errorf("postgres: connection is on database %q, not %q (metadata of another database needs a connection to it)", cur, db)
	}
	return cur, nil
}

// ListSchemas lists the schemas of db (c must be connected to db). User
// schemas come first; pg_catalog, information_schema and pg_toast are
// System. Temporary schemas (pg_temp_N, pg_toast_temp_N) are not listed.
func (p *Postgres) ListSchemas(ctx context.Context, c dialect.Conn, db string) ([]meta.Object, error) {
	if _, err := p.checkDatabase(ctx, c, db); err != nil {
		return nil, err
	}
	rows, err := c.QueryContext(ctx, `SELECT nspname, COALESCE(obj_description(oid, 'pg_namespace'), ''),
			nspname IN ('pg_catalog', 'information_schema') OR nspname LIKE 'pg\_toast%' AS sys
		FROM pg_namespace
		WHERE nspname NOT LIKE 'pg\_temp\_%' AND nspname NOT LIKE 'pg\_toast\_temp\_%'
		ORDER BY 3, nspname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []meta.Object
	for rows.Next() {
		var o meta.Object
		if err := rows.Scan(&o.Name, &o.Comment, &o.System); err != nil {
			return nil, err
		}
		o.Kind = meta.KindSchema
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListObjects lists the objects of schema at.Schema (default: the
// connection's current schema). Kinds: tables (incl. partitioned and
// foreign tables), views, materialized views, sequences (without the
// internal sequences of identity columns), functions and procedures (one
// entry per name, overloads merged, extension members omitted), and
// triggers (one entry per name; trigger names are unique per table only).
func (p *Postgres) ListObjects(ctx context.Context, c dialect.Conn, at meta.Path, kinds dialect.ObjectKinds) ([]meta.Object, error) {
	if _, err := p.checkDatabase(ctx, c, at.Database); err != nil {
		return nil, err
	}
	schema, err := p.schemaOf(ctx, c, at)
	if err != nil {
		return nil, err
	}
	var out []meta.Object
	scan := func(q string) error {
		rows, err := c.QueryContext(ctx, q, schema)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o meta.Object
			var kind string
			if err := rows.Scan(&o.Name, &kind, &o.Comment); err != nil {
				return err
			}
			o.Kind = meta.ObjectKind(kind)
			if kinds.Has(o.Kind) {
				out = append(out, o)
			}
		}
		return rows.Err()
	}
	if kinds.Has(meta.KindTable) || kinds.Has(meta.KindView) || kinds.Has(meta.KindMaterializedView) || kinds.Has(meta.KindSequence) {
		err := scan(`SELECT c.relname,
				CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view' WHEN 'S' THEN 'sequence' ELSE 'table' END,
				COALESCE(obj_description(c.oid, 'pg_class'), '')
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'f', 'v', 'm', 'S')
			  AND NOT (c.relkind = 'S' AND EXISTS (SELECT 1 FROM pg_depend d
				WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'i'))
			ORDER BY c.relname`)
		if err != nil {
			return nil, err
		}
	}
	if kinds.Has(meta.KindFunction) || kinds.Has(meta.KindProcedure) {
		err := scan(`SELECT p.proname, CASE WHEN p.prokind = 'p' THEN 'procedure' ELSE 'function' END,
				COALESCE(min(obj_description(p.oid, 'pg_proc')), '')
			FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = $1 AND p.prokind IN ('f', 'p')
			  AND NOT EXISTS (SELECT 1 FROM pg_depend d
				WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
			GROUP BY 1, 2 ORDER BY 1, 2`)
		if err != nil {
			return nil, err
		}
	}
	if kinds.Has(meta.KindTrigger) {
		err := scan(`SELECT t.tgname, 'trigger', COALESCE(min(obj_description(t.oid, 'pg_trigger')), '')
			FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND NOT t.tgisinternal
			GROUP BY 1 ORDER BY 1`)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// schemaOf returns at.Schema or the connection's current schema.
func (p *Postgres) schemaOf(ctx context.Context, c dialect.Conn, at meta.Path) (string, error) {
	if at.Schema != "" {
		return at.Schema, nil
	}
	s, err := p.CurrentSchema(ctx, c)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("postgres: no schema given and search_path has no existing schema")
	}
	return s, nil
}

// ChangeDatabase reports reconnect=true: a PostgreSQL connection cannot
// switch databases; the session reopens its connections on db.
func (p *Postgres) ChangeDatabase(ctx context.Context, c dialect.Conn, db string) (bool, error) {
	return true, nil
}

func (p *Postgres) CurrentDatabase(ctx context.Context, c dialect.Conn) (string, error) {
	var db string
	err := c.QueryRowContext(ctx, "SELECT current_database()").Scan(&db)
	return db, err
}

// ChangeSchema sets search_path to the schema followed by public (so that
// extension functions and types installed in public stay visible). The
// schema must exist. Like every SET, the change is rolled back with an
// open transaction that is rolled back.
func (p *Postgres) ChangeSchema(ctx context.Context, c dialect.Conn, schema string) error {
	var ok bool
	if err := c.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no such schema: %s", schema)
	}
	path := p.QuoteIdent(schema)
	if schema != "public" {
		path += ", public"
	}
	_, err := c.ExecContext(ctx, "SET search_path TO "+path)
	return err
}

func (p *Postgres) CurrentSchema(ctx context.Context, c dialect.Conn) (string, error) {
	var s sql.NullString
	err := c.QueryRowContext(ctx, "SELECT current_schema()").Scan(&s)
	return s.String, err
}

// QuoteIdent always double-quotes (correct for any name, including mixed
// case and keywords).
func (p *Postgres) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Qualify returns "schema"."object", or "object" without a schema (resolved
// through search_path). The database is implicit in the connection and
// never part of the name.
func (p *Postgres) Qualify(obj meta.Path) string {
	if obj.Schema == "" {
		return p.QuoteIdent(obj.Object)
	}
	return p.QuoteIdent(obj.Schema) + "." + p.QuoteIdent(obj.Object)
}

// LimitQuery limits query to n rows. A query with a top-level ORDER BY
// gets LIMIT appended, so the order decides which rows remain (an ORDER BY
// inside a derived table may be ignored); anything else is wrapped.
func (p *Postgres) LimitQuery(query string, n int) string {
	if q, ok := dialect.AppendLimit(sqltext.NewPostgres(), query, n); ok {
		return q
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS dbcc_limit LIMIT %d", strings.TrimRight(strings.TrimSpace(query), ";"), n)
}

func (p *Postgres) Splitter() sqltext.Splitter   { return sqltext.NewPostgres() }
func (p *Postgres) Tokenizer() sqltext.Tokenizer { return sqltext.NewPostgres() }

// LexerName is the chroma lexer ("postgresql"; PL/pgSQL bodies are
// coloured as strings).
func (p *Postgres) LexerName() string { return "postgresql" }

func (p *Postgres) SessionID(ctx context.Context, c dialect.Conn) (string, error) {
	var pid int64
	err := c.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid)
	return strconv.FormatInt(pid, 10), err
}

// Cancel returns dialect.ErrCancelViaContext: the session cancels the
// context of the running statement, and the connection's context watcher
// (see Open) sends a cancel request to the server. The statement fails with
// SQLSTATE 57014 and the pinned connection stays usable (verified by the
// integration test). CancelBackend is the explicit alternative.
func (p *Postgres) Cancel(ctx context.Context, aux dialect.Conn, sessionID string) error {
	return dialect.ErrCancelViaContext
}

// CancelBackend cancels the running statement of backend pid through
// pg_cancel_backend on aux (for `kill`-style use and as a fallback).
func (p *Postgres) CancelBackend(ctx context.Context, aux dialect.Conn, sessionID string) error {
	pid, err := strconv.ParseInt(sessionID, 10, 32)
	if err != nil {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	var ok bool
	if err := aux.QueryRowContext(ctx, "SELECT pg_cancel_backend($1)", pid).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("pg_cancel_backend(%d) failed", pid)
	}
	return nil
}

func (p *Postgres) SetReadOnly(ctx context.Context, c dialect.Conn) error {
	_, err := c.ExecContext(ctx, "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY")
	return err
}

// SetAutocommit is a no-op: PostgreSQL has no session autocommit switch;
// every statement outside BEGIN … COMMIT commits. For a manual-transaction
// profile the runner must send BEGIN before the first statement of each
// transaction (see ExplicitBegin).
func (p *Postgres) SetAutocommit(ctx context.Context, c dialect.Conn, on bool) error { return nil }

// noBegin lists first keywords of statements that the runner sends
// without a preceding BEGIN in a manual-transaction profile: transaction
// control itself and session commands that need no transaction.
var noBegin = map[string]bool{
	"BEGIN": true, "START": true, "COMMIT": true, "END": true, "ROLLBACK": true, "ABORT": true,
	"SAVEPOINT": true, "RELEASE": true, "SET": true, "RESET": true, "SHOW": true, "DISCARD": true,
	"LISTEN": true, "UNLISTEN": true, "CHECKPOINT": true, "DEALLOCATE": true,
}

// BeginFor implements dialect.TxnBeginner: PostgreSQL has no session
// autocommit switch, so a manual-transaction profile gets "BEGIN" before
// the first statement of each transaction. It returns "" for transaction
// control (BEGIN, COMMIT, ROLLBACK, SAVEPOINT, PREPARE TRANSACTION, …),
// session commands (SET, RESET, SHOW, DISCARD, …; but SET TRANSACTION and
// SET LOCAL do get BEGIN, they only work inside a transaction) and
// statements that cannot run in a transaction block (NoTransactionBlock).
func (p *Postgres) BeginFor(stmt string) string {
	kw := sqltext.Keywords(p.Tokenizer(), stmt, 2)
	if len(kw) == 0 {
		return ""
	}
	second := ""
	if len(kw) > 1 {
		second = kw[1]
	}
	switch {
	case kw[0] == "SET" && (second == "TRANSACTION" || second == "LOCAL" || second == "CONSTRAINTS"):
		return "BEGIN"
	case kw[0] == "PREPARE" && second == "TRANSACTION":
		return ""
	case noBegin[kw[0]] || p.NoTransactionBlock(stmt):
		return ""
	}
	return "BEGIN"
}

// TxnOpen implements dialect.TxnTracker from the protocol's transaction
// status (no round trip): open in 'T' (transaction) and 'E' (failed
// transaction, only ROLLBACK is accepted).
func (p *Postgres) TxnOpen(ctx context.Context, c dialect.Conn) (bool, error) {
	switch p.TxStatus(c) {
	case 'I':
		return false, nil
	case 'T', 'E':
		return true, nil
	}
	return false, errors.New("postgres: transaction status not available on this connection")
}

// MultiResult is always false: a PostgreSQL statement returns at most one
// result set (procedures return their INOUT parameters as one row).
func (p *Postgres) MultiResult(stmt string) bool { return false }

// TxStatus reports the server's transaction state of connection c (an
// *sql.Conn of this dialect): 'I' idle, 'T' in a transaction, 'E' in a
// failed transaction (only ROLLBACK is accepted). It returns 0 if c is not
// a pinned pgx connection.
func (p *Postgres) TxStatus(c dialect.Conn) byte {
	sc, ok := c.(*sql.Conn)
	if !ok {
		return 0
	}
	var st byte
	_ = sc.Raw(func(dc any) error {
		if pc, ok := dc.(*stdlib.Conn); ok {
			st = pc.Conn().PgConn().TxStatus()
		}
		return nil
	})
	return st
}

// ImplicitCommit is always false: PostgreSQL DDL is transactional.
func (p *Postgres) ImplicitCommit(stmt string) bool { return false }

// NoTransactionBlock reports statements that cannot run inside a
// transaction block (a manual-transaction runner must not send BEGIN before
// them): VACUUM, CREATE/DROP DATABASE, CREATE/DROP TABLESPACE, ALTER
// SYSTEM, … CONCURRENTLY index operations, CREATE/ALTER/DROP SUBSCRIPTION.
func (p *Postgres) NoTransactionBlock(stmt string) bool {
	kw := sqltext.Keywords(p.Tokenizer(), stmt, 0)
	if len(kw) == 0 {
		return false
	}
	has := func(w string) bool {
		for _, k := range kw {
			if k == w {
				return true
			}
		}
		return false
	}
	second := ""
	if len(kw) > 1 {
		second = kw[1]
	}
	switch kw[0] {
	case "VACUUM":
		return true
	case "CREATE", "DROP":
		switch second {
		case "DATABASE", "TABLESPACE", "SUBSCRIPTION":
			return true
		case "INDEX", "UNIQUE":
			return has("CONCURRENTLY")
		}
	case "ALTER":
		return second == "SYSTEM" || second == "SUBSCRIPTION"
	case "REINDEX":
		return has("CONCURRENTLY") || second == "DATABASE" || second == "SYSTEM"
	}
	return false
}

var rowStmts = map[string]bool{
	"SELECT": true, "WITH": true, "VALUES": true, "TABLE": true, "SHOW": true, "EXPLAIN": true,
	"FETCH": true, "CALL": true, "EXECUTE": true, "(": true,
}

// ReturnsRows reports statements run with Query: SELECT, WITH, VALUES,
// TABLE, SHOW, EXPLAIN, FETCH, CALL (procedures with OUT parameters return
// a row; without, the result has no columns), EXECUTE, a parenthesised
// query, and any statement with RETURNING.
func (p *Postgres) ReturnsRows(stmt string) bool {
	kw := sqltext.Keywords(p.Tokenizer(), stmt, 0)
	if len(kw) == 0 {
		return false
	}
	if rowStmts[kw[0]] {
		return true
	}
	for _, k := range kw {
		if k == "RETURNING" {
			return true
		}
	}
	return false
}

var readStmts = map[string]bool{
	"SELECT": true, "WITH": true, "VALUES": true, "TABLE": true, "SHOW": true, "EXPLAIN": true, "(": true,
	"FETCH": true, "MOVE": true, "CLOSE": true, "DECLARE": true,
	"SET": true, "RESET": true, "DISCARD": true, "LISTEN": true, "UNLISTEN": true,
	"BEGIN": true, "START": true, "COMMIT": true, "END": true, "ROLLBACK": true, "ABORT": true,
	"SAVEPOINT": true, "RELEASE": true,
}

// IsRead reports read statements for the read-only guard (best effort; the
// server setting is the real protection). SELECT/WITH/EXPLAIN containing
// INSERT, UPDATE, DELETE, MERGE or INTO (SELECT … INTO creates a table,
// FOR UPDATE locks rows) are writes, like on MySQL.
func (p *Postgres) IsRead(stmt string) bool {
	kw := sqltext.Keywords(p.Tokenizer(), stmt, 0)
	if len(kw) == 0 {
		return true
	}
	if !readStmts[kw[0]] {
		return false
	}
	switch kw[0] {
	case "SELECT", "WITH", "EXPLAIN", "(", "DECLARE":
		for _, k := range kw {
			switch k {
			case "INTO", "UPDATE", "DELETE", "INSERT", "MERGE":
				return false
			}
		}
	}
	return true
}

// ---- notices (Warnings) ----

const noticeKey = "dbcc.notices"

const maxNotices = 100

type notices struct {
	mu   sync.Mutex
	msgs []string
}

func (b *notices) add(n *pgconn.Notice) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.msgs) >= maxNotices {
		return
	}
	s := fmt.Sprintf("%s %s: %s", n.Severity, n.Code, n.Message)
	if n.Detail != "" {
		s += " (" + n.Detail + ")"
	}
	b.msgs = append(b.msgs, s)
}

func (b *notices) count() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.msgs) }

func (b *notices) drain() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.msgs
	b.msgs = nil
	return out
}

func noticesOf(c dialect.Conn) *notices {
	sc, ok := c.(*sql.Conn)
	if !ok {
		return nil
	}
	var b *notices
	_ = sc.Raw(func(dc any) error {
		if pc, ok := dc.(*stdlib.Conn); ok {
			b, _ = pc.Conn().PgConn().CustomData()[noticeKey].(*notices)
		}
		return nil
	})
	return b
}

// WarningCount returns the number of NOTICE/WARNING messages the server
// sent on c (a pinned *sql.Conn) since the last call of Warnings. Messages
// received while a lazy cursor was open are reported with the next
// statement.
func (p *Postgres) WarningCount(ctx context.Context, c dialect.Conn) (int, error) {
	if b := noticesOf(c); b != nil {
		return b.count(), nil
	}
	return 0, nil
}

// Warnings returns and clears the collected messages, formatted as
// "NOTICE 00000: message (detail)".
func (p *Postgres) Warnings(ctx context.Context, c dialect.Conn) ([]string, error) {
	if b := noticesOf(c); b != nil {
		return b.drain(), nil
	}
	return nil, nil
}
