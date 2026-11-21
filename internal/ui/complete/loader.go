// Package complete implements SQL and command-line completion for the TUI
// (spec 9.2): the Completer turns the completion context of internal/sqltext
// and the metadata cache of internal/meta into candidates, Popup is the
// on-demand candidate list shown at the editor cursor, and CommandLine
// completes commands and their typed arguments.
//
// This package also holds the adapters that connect the dialect-free
// meta.Cache to sessions (SessionLoader, WorkspaceLoaders) and the
// DDL-invalidation hook for exec.Runner.AfterStatement.
package complete

import (
	"context"

	"dbcc/internal/dialect"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/session"
	"dbcc/internal/sqltext"
)

// SessionLoader adapts a session to meta.Loader: every load runs through
// the session's dialect on its auxiliary connection (Session.AuxFor, so on
// PostgreSQL other databases get their own aux pool).
func SessionLoader(s *session.Session) meta.Loader { return sessionLoader{s} }

type sessionLoader struct{ s *session.Session }

func (l sessionLoader) Databases(ctx context.Context) ([]meta.Object, error) {
	return l.s.Dialect.ListDatabases(ctx, l.s.Aux())
}

func (l sessionLoader) Schemas(ctx context.Context, db string) ([]meta.Object, error) {
	if !l.s.Dialect.HasSchemas() {
		return nil, nil
	}
	c, err := l.s.AuxFor(ctx, db)
	if err != nil {
		return nil, err
	}
	return l.s.Dialect.ListSchemas(ctx, c, db)
}

func (l sessionLoader) Objects(ctx context.Context, at meta.Path) ([]meta.Object, error) {
	c, err := l.s.AuxFor(ctx, at.Database)
	if err != nil {
		return nil, err
	}
	return l.s.Dialect.ListObjects(ctx, c, at, nil)
}

func (l sessionLoader) Table(ctx context.Context, obj meta.Path) (*meta.Table, error) {
	c, err := l.s.AuxFor(ctx, obj.Database)
	if err != nil {
		return nil, err
	}
	return l.s.Dialect.DescribeTable(ctx, c, obj)
}

// WorkspaceLoaders returns a meta.LoaderFunc that serves the sessions open
// in ws. It never opens a connection: completion on a closed profile
// simply has no metadata. Pass it to meta.NewCache once at startup:
//
//	cache := meta.NewCache(complete.WorkspaceLoaders(ws))
func WorkspaceLoaders(ws *session.Workspace) meta.LoaderFunc {
	return func(conn string) (meta.Loader, bool) {
		s, ok := ws.Sessions()[conn]
		if !ok || s == nil {
			return nil, false
		}
		return SessionLoader(s), true
	}
}

// IsDDL reports whether stmt changes the catalog, by its first keyword
// (CREATE, ALTER, DROP, RENAME, TRUNCATE, COMMENT, IMPORT; meta.IsDDLKeyword).
func IsDDL(tok sqltext.Tokenizer, stmt string) bool {
	kw := sqltext.Keywords(tok, stmt, 1)
	return len(kw) == 1 && meta.IsDDLKeyword(kw[0])
}

// InvalidateOnDDL drops the cached metadata of connection conn when m is
// the message of a DDL statement (also a failed one: MySQL DDL is not
// transactional and may have changed things). It reports whether it did.
func InvalidateOnDDL(cache *meta.Cache, conn string, d dialect.Dialect, m exec.Message) bool {
	if cache == nil || d == nil || m.Stmt == "" || !IsDDL(d.Tokenizer(), m.Stmt) {
		return false
	}
	cache.InvalidateAll(conn)
	return true
}

// DDLInvalidator returns a hook for exec.Runner.AfterStatement that
// invalidates the cache of s's connection after DDL. next (may be nil) is
// called afterwards, so existing hooks can be chained:
//
//	r.AfterStatement = complete.DDLInvalidator(cache, r.Sess, r.AfterStatement)
func DDLInvalidator(cache *meta.Cache, s *session.Session, next func(exec.Message)) func(exec.Message) {
	return func(m exec.Message) {
		InvalidateOnDDL(cache, s.Profile.Name, s.Dialect, m)
		if next != nil {
			next(m)
		}
	}
}
