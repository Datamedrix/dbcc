// Package mysql implements the MySQL and MariaDB dialect.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	driver "github.com/go-sql-driver/mysql"

	"dbcc/internal/config"
	"dbcc/internal/dialect"
	"dbcc/internal/meta"
	"dbcc/internal/sqltext"
)

func init() { dialect.Register("mysql", &MySQL{}) }

// MySQL is the dialect for MySQL; with MariaDB set it is the MariaDB dialect.
type MySQL struct {
	MariaDB bool
	Version string
}

func (m *MySQL) Name() string {
	if m.MariaDB {
		return "mariadb"
	}
	return "mysql"
}

func (m *MySQL) Open(ctx context.Context, p *config.Profile, cred config.Credentials, database string) (*sql.DB, error) {
	cfg := driver.NewConfig()
	cfg.User = cred.User
	cfg.Passwd = cred.Password
	if cred.Socket != "" {
		cfg.Net = "unix"
		cfg.Addr = cred.Socket
	} else {
		cfg.Net = "tcp"
		host := cred.Host
		if host == "" {
			host = "127.0.0.1"
		}
		port := cred.Port
		if port == 0 {
			port = 3306
		}
		cfg.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	}
	cfg.DBName = database
	cfg.MultiStatements = false // the tool splits statements itself (spec 5)
	cfg.AllowNativePasswords = true
	cfg.Timeout = 10 * time.Second
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	for k, v := range p.Params {
		switch k {
		case "tls":
			cfg.TLSConfig = v
		default:
			cfg.Params[k] = v
		}
	}
	conn, err := driver.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(conn), nil
}

func (m *MySQL) Detect(ctx context.Context, c dialect.Conn) (dialect.Dialect, error) {
	var v string
	if err := c.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v); err != nil {
		return nil, err
	}
	return &MySQL{MariaDB: strings.Contains(strings.ToLower(v), "mariadb"), Version: v}, nil
}

func (m *MySQL) HasSchemas() bool { return false }

var systemDBs = map[string]bool{"information_schema": true, "mysql": true, "performance_schema": true, "sys": true}

func (m *MySQL) ListDatabases(ctx context.Context, c dialect.Conn) ([]meta.Object, error) {
	rows, err := c.QueryContext(ctx, "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var user, sys []meta.Object
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		o := meta.Object{Name: n, Kind: meta.KindDatabase, System: systemDBs[strings.ToLower(n)]}
		if o.System {
			sys = append(sys, o)
		} else {
			user = append(user, o)
		}
	}
	return append(user, sys...), rows.Err()
}

func (m *MySQL) ListSchemas(ctx context.Context, c dialect.Conn, db string) ([]meta.Object, error) {
	return nil, nil
}

func (m *MySQL) ListObjects(ctx context.Context, c dialect.Conn, at meta.Path, kinds dialect.ObjectKinds) ([]meta.Object, error) {
	db := at.Database
	var out []meta.Object
	scan := func(q string, kindOf func(string) meta.ObjectKind) error {
		rows, err := c.QueryContext(ctx, q, db)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name, typ, comment string
			if err := rows.Scan(&name, &typ, &comment); err != nil {
				return err
			}
			kind := kindOf(typ)
			if kind == meta.KindView && comment == "VIEW" {
				comment = "" // information_schema.TABLES reports "VIEW" as the comment of every view
			}
			out = append(out, meta.Object{Name: name, Kind: kind, Comment: comment})
		}
		return rows.Err()
	}
	if kinds.Has(meta.KindTable) || kinds.Has(meta.KindView) || kinds.Has(meta.KindSequence) {
		err := scan(`SELECT TABLE_NAME, TABLE_TYPE, COALESCE(TABLE_COMMENT,'') FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME`, func(t string) meta.ObjectKind {
			switch t {
			case "VIEW", "SYSTEM VIEW":
				return meta.KindView
			case "SEQUENCE":
				return meta.KindSequence
			}
			return meta.KindTable
		})
		if err != nil {
			return nil, err
		}
	}
	if kinds.Has(meta.KindProcedure) || kinds.Has(meta.KindFunction) {
		err := scan(`SELECT ROUTINE_NAME, ROUTINE_TYPE, COALESCE(ROUTINE_COMMENT,'') FROM information_schema.ROUTINES
			WHERE ROUTINE_SCHEMA = ? ORDER BY ROUTINE_NAME`, func(t string) meta.ObjectKind {
			if t == "FUNCTION" {
				return meta.KindFunction
			}
			return meta.KindProcedure
		})
		if err != nil {
			return nil, err
		}
	}
	if kinds.Has(meta.KindTrigger) {
		if err := scan(`SELECT TRIGGER_NAME, 'TRIGGER', '' FROM information_schema.TRIGGERS
			WHERE TRIGGER_SCHEMA = ? ORDER BY TRIGGER_NAME`, func(string) meta.ObjectKind { return meta.KindTrigger }); err != nil {
			return nil, err
		}
	}
	if kinds.Has(meta.KindEvent) {
		if err := scan(`SELECT EVENT_NAME, 'EVENT', COALESCE(EVENT_COMMENT,'') FROM information_schema.EVENTS
			WHERE EVENT_SCHEMA = ? ORDER BY EVENT_NAME`, func(string) meta.ObjectKind { return meta.KindEvent }); err != nil {
			return nil, err
		}
	}
	filtered := out[:0]
	for _, o := range out {
		if kinds.Has(o.Kind) {
			filtered = append(filtered, o)
		}
	}
	return filtered, nil
}

func (m *MySQL) ChangeDatabase(ctx context.Context, c dialect.Conn, db string) (bool, error) {
	_, err := c.ExecContext(ctx, "USE "+m.QuoteIdent(db))
	return false, err
}

func (m *MySQL) CurrentDatabase(ctx context.Context, c dialect.Conn) (string, error) {
	var db sql.NullString
	err := c.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&db)
	return db.String, err
}

func (m *MySQL) ChangeSchema(ctx context.Context, c dialect.Conn, schema string) error {
	return fmt.Errorf("%s has no schema level", m.Name())
}

func (m *MySQL) CurrentSchema(ctx context.Context, c dialect.Conn) (string, error) { return "", nil }

func (m *MySQL) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (m *MySQL) Qualify(obj meta.Path) string {
	if obj.Database == "" {
		return m.QuoteIdent(obj.Object)
	}
	return m.QuoteIdent(obj.Database) + "." + m.QuoteIdent(obj.Object)
}

// LimitQuery limits query to n rows. A query with a top-level ORDER BY
// gets LIMIT appended, so the order decides which rows remain (an ORDER BY
// inside a derived table may be ignored); anything else is wrapped.
func (m *MySQL) LimitQuery(query string, n int) string {
	if q, ok := dialect.AppendLimit(sqltext.NewMySQL(), query, n); ok {
		return q
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS dbcc_limit LIMIT %d", strings.TrimRight(strings.TrimSpace(query), ";"), n)
}

func (m *MySQL) Splitter() sqltext.Splitter   { return sqltext.NewMySQL() }
func (m *MySQL) Tokenizer() sqltext.Tokenizer { return sqltext.NewMySQL() }
func (m *MySQL) LexerName() string            { return "mysql" }

func (m *MySQL) SessionID(ctx context.Context, c dialect.Conn) (string, error) {
	var id int64
	err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id)
	return strconv.FormatInt(id, 10), err
}

func (m *MySQL) Cancel(ctx context.Context, aux dialect.Conn, sessionID string) error {
	if _, err := strconv.ParseInt(sessionID, 10, 64); err != nil {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	_, err := aux.ExecContext(ctx, "KILL QUERY "+sessionID)
	return err
}

func (m *MySQL) SetReadOnly(ctx context.Context, c dialect.Conn) error {
	_, err := c.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY")
	return err
}

func (m *MySQL) SetAutocommit(ctx context.Context, c dialect.Conn, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	_, err := c.ExecContext(ctx, "SET autocommit = "+v)
	return err
}

// implicitCommitFirst lists leading keywords of statements that cause an
// implicit commit in MySQL and MariaDB.
var implicitCommitFirst = map[string]bool{
	"CREATE": true, "ALTER": true, "DROP": true, "RENAME": true, "TRUNCATE": true,
	"GRANT": true, "REVOKE": true, "LOCK": true, "UNLOCK": true, "INSTALL": true,
	"UNINSTALL": true, "ANALYZE": true, "OPTIMIZE": true, "REPAIR": true,
	"CACHE": true, "FLUSH": true, "RESET": true, "LOAD": true,
}

func (m *MySQL) ImplicitCommit(stmt string) bool {
	kw := sqltext.Keywords(m.Tokenizer(), stmt, 2)
	if len(kw) == 0 {
		return false
	}
	if kw[0] == "CREATE" && len(kw) > 1 && kw[1] == "TEMPORARY" {
		return false
	}
	if kw[0] == "SET" && len(kw) > 1 && kw[1] == "PASSWORD" {
		return true
	}
	return implicitCommitFirst[kw[0]]
}

var rowStmts = map[string]bool{
	"SELECT": true, "SHOW": true, "DESC": true, "DESCRIBE": true, "EXPLAIN": true,
	"WITH": true, "CALL": true, "VALUES": true, "TABLE": true, "HANDLER": true,
	"CHECK": true, "CHECKSUM": true, "ANALYZE": true, "OPTIMIZE": true, "REPAIR": true,
	"(": true, "HELP": true, "EXECUTE": true,
}

func (m *MySQL) ReturnsRows(stmt string) bool {
	toks := sqltext.Keywords(m.Tokenizer(), stmt, 0)
	if len(toks) == 0 {
		return false
	}
	if rowStmts[toks[0]] {
		return true
	}
	for _, t := range toks {
		if t == "RETURNING" { // MariaDB INSERT/DELETE … RETURNING
			return true
		}
	}
	return false
}

// MultiResult reports CALL: a procedure may return several result sets.
func (m *MySQL) MultiResult(stmt string) bool {
	kw := sqltext.Keywords(m.Tokenizer(), stmt, 1)
	return len(kw) == 1 && kw[0] == "CALL"
}

var readStmts = map[string]bool{
	"SELECT": true, "SHOW": true, "DESC": true, "DESCRIBE": true, "EXPLAIN": true,
	"WITH": true, "VALUES": true, "TABLE": true, "USE": true, "HELP": true, "(": true,
	"COMMIT": true, "ROLLBACK": true, "BEGIN": true, "START": true, "SET": true,
}

func (m *MySQL) IsRead(stmt string) bool {
	toks := sqltext.Keywords(m.Tokenizer(), stmt, 0)
	if len(toks) == 0 {
		return true
	}
	if !readStmts[toks[0]] {
		return false
	}
	if toks[0] == "SELECT" || toks[0] == "WITH" {
		for _, t := range toks {
			if t == "INTO" || t == "UPDATE" || t == "DELETE" || t == "INSERT" {
				return false // SELECT … INTO OUTFILE / WITH … UPDATE
			}
		}
	}
	return true
}

func (m *MySQL) Warnings(ctx context.Context, c dialect.Conn) ([]string, error) {
	rows, err := c.QueryContext(ctx, "SHOW WARNINGS")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var level, msg string
		var code int
		if err := rows.Scan(&level, &code, &msg); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s %d: %s", level, code, msg))
	}
	return out, rows.Err()
}

// WarningCount reports whether the driver can tell the warning count; the
// go-sql-driver does not expose it, so the runner queries @@warning_count.
func (m *MySQL) WarningCount(ctx context.Context, c dialect.Conn) (int, error) {
	var n int
	err := c.QueryRowContext(ctx, "SELECT @@warning_count").Scan(&n)
	return n, err
}

// ReplaceWarning implements dialect.ReplaceWarner (A21, verified on MySQL
// 8.4): MySQL replaces routines, triggers and events with DROP + CREATE.
// MariaDB uses CREATE OR REPLACE and keeps privileges.
func (m *MySQL) ReplaceWarning(kind meta.ObjectKind) string {
	if m.MariaDB || kind == meta.KindView {
		return ""
	}
	return "MySQL replaces this object with DROP + CREATE: this is not atomic, and privileges granted on a routine are removed when automatic_sp_privileges = 1 (the default)."
}
