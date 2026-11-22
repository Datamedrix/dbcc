// Package mssql implements the Microsoft SQL Server dialect (spec M10).
//
// Conventions that differ from the MySQL dialect and that callers must
// know:
//
//   - Placeholders: go-mssqldb binds @p1 … @pN (positional arguments are
//     numbered from 1 in call order; "?" is NOT rewritten). A placeholder
//     may appear several times in one statement and binds the same value.
//     Lua commands and the grid must therefore generate @pN, not "?".
//   - A "statement" handed to the runner is a whole GO batch, which may
//     contain several T-SQL statements and result sets (see ReturnsRows).
//   - Schemas: HasSchemas is true. T-SQL has no session default schema
//     that can be switched, so ChangeSchema only validates the schema;
//     the schema of the dbcc path is used to qualify names (Qualify) and
//     unqualified names in the owner's own SQL resolve against the login's
//     default schema (usually dbo).
//   - Metadata queries use three-part names ([db].sys.objects …), so they
//     work on the auxiliary connection whatever database it is in.
package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	driver "github.com/microsoft/go-mssqldb"

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

func init() { dialect.Register("mssql", &MSSQL{}) }

var _ dialect.Dialect = (*MSSQL)(nil)

// MSSQL is the SQL Server dialect. Detect fills in the server version.
type MSSQL struct {
	Version string // SERVERPROPERTY('ProductVersion'), e.g. 16.0.4250.1
	Major   int    // 13 = 2016, 14 = 2017, 15 = 2019, 16 = 2022; 0 = unknown (treated as new)
	// Cursor selects the statement-at-cursor scope (A10); the zero value is
	// sqltext.CursorStatement.
	Cursor sqltext.CursorScope
}

func (m *MSSQL) Name() string { return "mssql" }

// atLeast reports whether the server is at least the given major version.
// An unknown version counts as new.
func (m *MSSQL) atLeast(major int) bool { return m.Major == 0 || m.Major >= major }

// Open builds a go-mssqldb connector. Host may be "host\instance" (port 0
// then lets the SQL Browser resolve the instance). Profile params are
// passed through as DSN parameters, e.g. encrypt = "disable",
// TrustServerCertificate = "true", ApplicationIntent = "ReadOnly",
// "connection timeout" = "30". The application name is "dbcc" unless the
// params set "app name".
func (m *MSSQL) Open(ctx context.Context, p *config.Profile, cred config.Credentials, database string) (*sql.DB, error) {
	host := cred.Host
	if host == "" {
		host = "127.0.0.1"
	}
	instance := ""
	if i := strings.IndexByte(host, '\\'); i >= 0 {
		host, instance = host[:i], host[i+1:]
	}
	if instance != "" && cred.Port == 0 && cred.Dial != nil {
		return nil, fmt.Errorf("SQL Server instance %q through SSH: give the port (the SQL Browser service cannot be reached through a tunnel)", instance)
	}
	u := &url.URL{Scheme: "sqlserver", User: url.UserPassword(cred.User, cred.Password)}
	switch {
	case instance != "" && cred.Port == 0:
		u.Host = host
	case cred.Port == 0:
		u.Host = net.JoinHostPort(host, "1433")
	default:
		u.Host = net.JoinHostPort(host, strconv.Itoa(cred.Port))
	}
	if instance != "" {
		u.Path = "/" + instance
	}
	q := url.Values{}
	q.Set("app name", "dbcc")
	q.Set("dial timeout", "10")
	if database != "" {
		q.Set("database", database)
	}
	for k, v := range p.Params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	conn, err := driver.NewConnector(u.String())
	if err != nil {
		return nil, err
	}
	if cred.Dial != nil {
		conn.Dialer = tunnelDialer{dial: cred.Dial, host: host} // SSH tunnel (A27)
	}
	return sql.OpenDB(conn), nil
}

// tunnelDialer dials through the SSH tunnel; as a driver.HostDialer it
// makes the driver leave name resolution to the far side.
type tunnelDialer struct {
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	host string
}

func (d tunnelDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dial(ctx, network, addr)
}

func (d tunnelDialer) HostName() string { return d.host }

// Detect reads the product version.
func (m *MSSQL) Detect(ctx context.Context, c dialect.Conn) (dialect.Dialect, error) {
	var v string
	if err := c.QueryRowContext(ctx, "SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128))").Scan(&v); err != nil {
		return nil, err
	}
	major, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return &MSSQL{Version: v, Major: major, Cursor: m.Cursor}, nil
}

func (m *MSSQL) HasSchemas() bool { return true }

var systemDBs = map[string]bool{"master": true, "model": true, "msdb": true, "tempdb": true}

// ListDatabases lists sys.databases, user databases first. Databases that
// are not ONLINE carry their state as comment.
func (m *MSSQL) ListDatabases(ctx context.Context, c dialect.Conn) ([]meta.Object, error) {
	rows, err := c.QueryContext(ctx, "SELECT name, state_desc FROM sys.databases ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var user, sys []meta.Object
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			return nil, err
		}
		o := meta.Object{Name: name, Kind: meta.KindDatabase, System: systemDBs[strings.ToLower(name)]}
		if state != "ONLINE" {
			o.Comment = state
		}
		if o.System {
			sys = append(sys, o)
		} else {
			user = append(user, o)
		}
	}
	return append(user, sys...), rows.Err()
}

// isSystemSchema reports the built-in schemas: sys, INFORMATION_SCHEMA,
// guest and the fixed database roles (db_owner, …; schema_id >= 16384).
func isSystemSchema(name string, id int) bool {
	switch strings.ToLower(name) {
	case "sys", "information_schema", "guest":
		return true
	}
	return id >= 16384
}

// sysPrefix returns "[db].sys." for metadata queries.
func (m *MSSQL) sysPrefix(db string) string {
	if db == "" {
		return "sys."
	}
	return m.QuoteIdent(db) + ".sys."
}

// ListSchemas lists the schemas of db, user schemas first.
func (m *MSSQL) ListSchemas(ctx context.Context, c dialect.Conn, db string) ([]meta.Object, error) {
	x := m.sysPrefix(db)
	rows, err := c.QueryContext(ctx, `SELECT s.name, s.schema_id, COALESCE(CAST(ep.value AS nvarchar(max)), '')
		FROM `+x+`schemas s
		LEFT JOIN `+x+`extended_properties ep ON ep.class = 3 AND ep.major_id = s.schema_id AND ep.minor_id = 0 AND ep.name = 'MS_Description'
		ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var user, sys []meta.Object
	for rows.Next() {
		var o meta.Object
		var id int
		if err := rows.Scan(&o.Name, &id, &o.Comment); err != nil {
			return nil, err
		}
		o.Kind = meta.KindSchema
		o.System = isSystemSchema(o.Name, id)
		if o.System {
			sys = append(sys, o)
		} else {
			user = append(user, o)
		}
	}
	return append(user, sys...), rows.Err()
}

// objectKind maps sys.objects.type to a kind ("" for other types).
func objectKind(typ string) meta.ObjectKind {
	switch strings.TrimSpace(typ) {
	case "U":
		return meta.KindTable
	case "V":
		return meta.KindView
	case "P", "PC", "X", "RF":
		return meta.KindProcedure
	case "FN", "IF", "TF", "FS", "FT":
		return meta.KindFunction
	case "TR", "TA":
		return meta.KindTrigger
	case "SO":
		return meta.KindSequence
	}
	return ""
}

// kindTypes are the sys.objects types of each kind.
var kindTypes = map[meta.ObjectKind]string{
	meta.KindTable:     "'U'",
	meta.KindView:      "'V'",
	meta.KindProcedure: "'P','PC','X','RF'",
	meta.KindFunction:  "'FN','IF','TF','FS','FT'",
	meta.KindTrigger:   "'TR','TA'",
	meta.KindSequence:  "'SO'",
}

// ListObjects lists tables, views, procedures, functions (scalar, inline
// and multi-statement table-valued, CLR), DML triggers and sequences of
// at.Schema in at.Database (empty schema: the connection's default
// schema), without Microsoft-shipped objects. Comments come from the
// MS_Description extended property.
func (m *MSSQL) ListObjects(ctx context.Context, c dialect.Conn, at meta.Path, kinds dialect.ObjectKinds) ([]meta.Object, error) {
	var types []string
	for _, k := range []meta.ObjectKind{meta.KindTable, meta.KindView, meta.KindProcedure, meta.KindFunction, meta.KindTrigger, meta.KindSequence} {
		if kinds.Has(k) {
			types = append(types, kindTypes[k])
		}
	}
	if len(types) == 0 {
		return nil, nil
	}
	x := m.sysPrefix(at.Database)
	schemaCond := "s.name = @p1"
	args := []any{at.Schema}
	if at.Schema == "" {
		schemaCond, args = "s.name = SCHEMA_NAME()", nil
	}
	rows, err := c.QueryContext(ctx, `SELECT o.name, o.type, COALESCE(CAST(ep.value AS nvarchar(max)), '')
		FROM `+x+`objects o
		JOIN `+x+`schemas s ON s.schema_id = o.schema_id
		LEFT JOIN `+x+`extended_properties ep ON ep.class = 1 AND ep.major_id = o.object_id AND ep.minor_id = 0 AND ep.name = 'MS_Description'
		WHERE `+schemaCond+` AND o.is_ms_shipped = 0 AND o.type IN (`+strings.Join(types, ",")+`)
		ORDER BY o.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []meta.Object
	for rows.Next() {
		var o meta.Object
		var typ string
		if err := rows.Scan(&o.Name, &typ, &o.Comment); err != nil {
			return nil, err
		}
		o.Kind = objectKind(typ)
		out = append(out, o)
	}
	return out, rows.Err()
}

// ChangeDatabase issues USE [db] on the main session (no reconnect).
func (m *MSSQL) ChangeDatabase(ctx context.Context, c dialect.Conn, db string) (bool, error) {
	_, err := c.ExecContext(ctx, "USE "+m.QuoteIdent(db))
	return false, err
}

func (m *MSSQL) CurrentDatabase(ctx context.Context, c dialect.Conn) (string, error) {
	var db sql.NullString
	err := c.QueryRowContext(ctx, "SELECT DB_NAME()").Scan(&db)
	return db.String, err
}

// ChangeSchema only checks that the schema exists in the session's current
// database: T-SQL has no switchable default schema. The session keeps the
// schema in its path, and dbcc qualifies names with it.
func (m *MSSQL) ChangeSchema(ctx context.Context, c dialect.Conn, schema string) error {
	var n int
	err := c.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.schemas WHERE name = @p1", schema).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: schema %s", meta.ErrNotFound, m.QuoteIdent(schema))
	}
	return nil
}

// CurrentSchema returns the default schema of the login in the current
// database (SCHEMA_NAME()).
func (m *MSSQL) CurrentSchema(ctx context.Context, c dialect.Conn) (string, error) {
	var s sql.NullString
	err := c.QueryRowContext(ctx, "SELECT SCHEMA_NAME()").Scan(&s)
	return s.String, err
}

// QuoteIdent quotes with brackets; "]" is doubled.
func (m *MSSQL) QuoteIdent(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

// Qualify returns [db].[schema].[obj], [db]..[obj] (default schema) when
// only the database is known, [schema].[obj] or [obj]. Three-part names
// work in SELECT, INSERT, UPDATE, DELETE, CREATE TABLE, CREATE INDEX …
// ON, ALTER TABLE and SET IDENTITY_INSERT, but not in CREATE VIEW,
// PROCEDURE, FUNCTION or TRIGGER (see ReplaceScript, CopyTable).
func (m *MSSQL) Qualify(obj meta.Path) string {
	name := m.QuoteIdent(obj.Object)
	switch {
	case obj.Database != "" && obj.Schema != "":
		return m.QuoteIdent(obj.Database) + "." + m.QuoteIdent(obj.Schema) + "." + name
	case obj.Database != "":
		return m.QuoteIdent(obj.Database) + ".." + name
	case obj.Schema != "":
		return m.QuoteIdent(obj.Schema) + "." + name
	}
	return name
}

func (m *MSSQL) Splitter() sqltext.Splitter   { return &sqltext.MSSQL{Cursor: m.Cursor} }
func (m *MSSQL) Tokenizer() sqltext.Tokenizer { return sqltext.NewMSSQL() }
func (m *MSSQL) LexerName() string            { return "tsql" }

// SessionID returns @@SPID.
func (m *MSSQL) SessionID(ctx context.Context, c dialect.Conn) (string, error) {
	var id int64
	err := c.QueryRowContext(ctx, "SELECT CAST(@@SPID AS bigint)").Scan(&id)
	return strconv.FormatInt(id, 10), err
}

// Cancel returns dialect.ErrCancelViaContext: go-mssqldb sends a TDS
// attention when the statement's context is cancelled, the server stops
// the batch, and the pinned connection stays usable (verified by
// TestCancel). KILL would end the whole session, so it is not used.
func (m *MSSQL) Cancel(ctx context.Context, aux dialect.Conn, sessionID string) error {
	return dialect.ErrCancelViaContext
}

// SetReadOnly does nothing: SQL Server has no read-only session setting.
// Protection comes from the client-side guard (IsRead) and from the
// login's permissions; on an availability group listener the profile can
// add params ApplicationIntent = "ReadOnly" to be routed to a readable
// secondary.
func (m *MSSQL) SetReadOnly(ctx context.Context, c dialect.Conn) error { return nil }

// SetAutocommit switches implicit transactions: off (manual mode) runs
// SET IMPLICIT_TRANSACTIONS ON, so the first data or DDL statement opens a
// transaction that stays open until COMMIT or ROLLBACK.
func (m *MSSQL) SetAutocommit(ctx context.Context, c dialect.Conn, on bool) error {
	v := "ON"
	if on {
		v = "OFF"
	}
	_, err := c.ExecContext(ctx, "SET IMPLICIT_TRANSACTIONS "+v)
	return err
}

// ImplicitCommit is always false: DDL is transactional in SQL Server.
// Statements that cannot run in a transaction (CREATE DATABASE, BACKUP, …)
// fail instead of committing.
func (m *MSSQL) ImplicitCommit(stmt string) bool { return false }

// WarningCount is always 0: go-mssqldb delivers informational messages
// (PRINT, RAISERROR with severity <= 10) only through the sqlexp message
// loop, which the runner does not use (yet).
func (m *MSSQL) WarningCount(ctx context.Context, c dialect.Conn) (int, error) { return 0, nil }

func (m *MSSQL) Warnings(ctx context.Context, c dialect.Conn) ([]string, error) { return nil, nil }

// TxnOpen implements dialect.TxnTracker: @@TRANCOUNT > 0. It tracks
// batches such as "INSERT …; COMMIT" and transactions opened implicitly
// (IMPLICIT_TRANSACTIONS ON in manual-transaction profiles).
func (m *MSSQL) TxnOpen(ctx context.Context, c dialect.Conn) (bool, error) {
	var n int
	if err := c.QueryRowContext(ctx, "SELECT @@TRANCOUNT").Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// Placeholder returns "@pN", go-mssqldb's positional placeholder.
func (m *MSSQL) Placeholder(n int) string { return "@p" + strconv.Itoa(n) }

// InsertDefaults is the standard all-defaults INSERT.
func (m *MSSQL) InsertDefaults(qualifiedTable string) string {
	return "INSERT INTO " + qualifiedTable + " DEFAULT VALUES"
}

// codeWords returns the upper-cased bare words and the punctuation "(",
// ")" and ";" of stmt, in order.
func codeWords(stmt string) []string {
	var out []string
	for _, t := range sqltext.NewMSSQL().Tokenize(stmt) {
		switch t.Kind {
		case sqltext.TokIdent:
			out = append(out, strings.ToUpper(t.Text))
		case sqltext.TokPunct:
			if t.Text == "(" || t.Text == ")" || t.Text == ";" {
				out = append(out, t.Text)
			}
		case sqltext.TokQuotedIdent, sqltext.TokString, sqltext.TokNumber, sqltext.TokParam:
			out = append(out, "\x00") // a non-keyword operand
		}
	}
	return out
}

// statementStart lists words that begin a new statement at the same level
// (they end the INSERT whose SELECT or EXEC feeds no result to the client).
var statementStart = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "SELECT": true, "EXEC": true,
	"EXECUTE": true, "SET": true, "DECLARE": true, "IF": true, "WHILE": true, "BEGIN": true, "END": true,
	"PRINT": true, "RETURN": true, "CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true,
	"COMMIT": true, "ROLLBACK": true, "WITH": true, "USE": true, "RAISERROR": true, "THROW": true,
}

// ReturnsRows decides between Query and Exec for a batch. A batch can
// return result sets from any of its statements, so the test is: can any
// statement of the batch produce rows? That is the case for a SELECT at
// parenthesis depth 0 (subqueries, IF EXISTS (SELECT …) and CTE bodies
// do not count; neither does the SELECT or EXEC that feeds an INSERT),
// EXEC/EXECUTE (procedures may return sets), OUTPUT clauses, DBCC,
// RESTORE (HEADERONLY, FILELISTONLY, …), FETCH, READTEXT and a batch that
// starts with a procedure name (implicit EXEC of sp_…, xp_…).
//
// Runner strategy: Query reads every result set (NextResultSet) but loses
// the rows-affected count; Exec keeps the count and discards rows. So the
// runner uses Exec exactly when no statement can return rows, which is
// what this function reports. A batch mixing DML and SELECT goes through
// Query; the rows-affected count of its DML is then not reported. False
// positives (SELECT @v = …) only cost that count.
func (m *MSSQL) ReturnsRows(stmt string) bool { return rowSources(stmt) > 0 }

// MultiResult reports batches that may return several result sets: more
// than one SELECT at depth 0 (or other row source), or any EXEC, DBCC or
// implicit procedure call (a procedure may return any number of sets).
func (m *MSSQL) MultiResult(stmt string) bool { return rowSources(stmt) > 1 }

// manySets is the rowSources count of a source that may return several
// result sets by itself.
const manySets = 2

// rowSources counts the statements of a batch that can return a result
// set to the client (see ReturnsRows); a source that may return any number
// of sets (EXEC, DBCC, an implicit procedure call) counts as manySets.
func rowSources(stmt string) int {
	ws := codeWords(stmt)
	if len(ws) == 0 {
		return 0
	}
	n := 0
	if strings.HasPrefix(ws[0], "SP_") || strings.HasPrefix(ws[0], "XP_") {
		n += manySets
	}
	depth := 0
	insertOpen, fed := false, false // INSERT at depth 0 and whether its source was seen
	prev := ""
	for i, w := range ws {
		switch {
		case w == "(":
			if i == 0 {
				n++ // a parenthesised query
			}
			depth++
		case w == ")":
			if depth > 0 {
				depth--
			}
		case w == ";":
			insertOpen = false
		case w == "OUTPUT" && prev != "\x00": // not "@param OUTPUT" of EXEC
			n++
		case w == "DBCC":
			n += manySets
		case w == "FETCH" || w == "READTEXT" || w == "RESTORE":
			n++
		case depth > 0:
		case w == "INSERT":
			insertOpen, fed = true, false
		case w == "VALUES" && insertOpen:
			fed = true
		case w == "SELECT" || w == "EXEC" || w == "EXECUTE":
			if insertOpen && (!fed || prev == "UNION" || prev == "ALL" || prev == "EXCEPT" || prev == "INTERSECT") {
				fed = true // the INSERT's source
				break
			}
			if w == "SELECT" {
				// UNION / EXCEPT / INTERSECT continue the same query.
				if prev != "UNION" && prev != "ALL" && prev != "EXCEPT" && prev != "INTERSECT" {
					n++
				}
			} else {
				n += manySets
			}
		case insertOpen && fed && statementStart[w]:
			insertOpen = false
		}
		prev = w
	}
	return n
}

// readFirst are the first words of batches that may be reads; the whole
// batch must also be free of writeWords.
var readFirst = map[string]bool{
	"SELECT": true, "WITH": true, "SET": true, "USE": true, "DECLARE": true, "PRINT": true,
	"IF": true, "BEGIN": true, "WHILE": true, "COMMIT": true, "ROLLBACK": true, "SAVE": true,
	"(": true, "WAITFOR": true, "RAISERROR": true, "THROW": true, "RETURN": true,
	"OPEN": true, "FETCH": true, "CLOSE": true, "DEALLOCATE": true, "READTEXT": true,
}

var writeWords = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true, "CREATE": true, "ALTER": true,
	"DROP": true, "TRUNCATE": true, "EXEC": true, "EXECUTE": true, "GRANT": true, "REVOKE": true,
	"DENY": true, "BULK": true, "BACKUP": true, "RESTORE": true, "DBCC": true, "KILL": true,
	"SHUTDOWN": true, "RECONFIGURE": true, "INTO": true, "WRITETEXT": true, "UPDATETEXT": true,
	"ENABLE": true, "DISABLE": true, "CHECKPOINT": true, "SEND": true, "RECEIVE": true,
	"MOVE": true, "GET": true, "OPENQUERY": true, "OPENROWSET": true,
}

// IsRead is the read-only guard (best effort, spec 12): the batch's first
// word must start a read statement and no word of the whole batch may be a
// writing keyword (so SELECT … INTO, EXEC and DECLARE … INSERT count as
// writes). SET counts as read (session options).
func (m *MSSQL) IsRead(stmt string) bool {
	ws := codeWords(stmt)
	if len(ws) == 0 {
		return true
	}
	if !readFirst[ws[0]] {
		return false
	}
	for _, w := range ws {
		if writeWords[w] {
			return false
		}
	}
	return true
}
