//go:build integration

package app

import (
	"context"
	"testing"
	"time"

	"dbcc/internal/exec"
)

// bigQuery streams 1,000,000 rows of 2 KB without needing a table.
func bigQuery(server string) string {
	switch server {
	case "postgres":
		return "SELECT g, repeat('x', 2000) AS pad FROM generate_series(1, 1000000) g"
	case "mssql":
		return "WITH d AS (SELECT n FROM (VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) v(n)) " +
			"SELECT REPLICATE('x', 2000) AS pad FROM d a, d b, d c, d e, d f, d g"
	default:
		return "WITH d AS (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 " +
			"UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) " +
			"SELECT REPEAT('x', 2000) AS pad FROM d a, d b, d c, d e, d f, d g"
	}
}

// TestNewStatementAbortsUnreadStream: a lazily loaded result that the
// server is still streaming must not block the next statement (closing a
// cursor reads and discards every unread row — minutes on large tables;
// the UI looked frozen). The stream is aborted on the server and the pinned
// session survives.
func TestNewStatementAbortsUnreadStream(t *testing.T) {
	for _, srv := range allServers(t) {
		t.Run(srv.name, func(t *testing.T) {
			srv.prepare(t)
			s := openIt(t, srv.cfg(t, srv.db, ""), "it")
			ctx := context.Background()
			idBefore, err := s.Dialect.SessionID(ctx, s.Main())
			if err != nil {
				t.Fatal(err)
			}
			r := exec.NewRunner(s, &countSink{})
			r.FetchSize = 100
			res, err := r.Grid(ctx, bigQuery(srv.name), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !res.More() {
				t.Fatal("expected a lazily loaded result")
			}
			start := time.Now()
			if _, err := r.Query(ctx, "SELECT 1"); err != nil {
				t.Fatalf("next statement after aborted stream: %v", err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("next statement waited %v for the unread stream", d)
			} else {
				t.Logf("next statement after %v", d)
			}
			if !res.Partial || res.More() {
				t.Errorf("old result: partial=%v more=%v", res.Partial, res.More())
			}
			idAfter, err := s.Dialect.SessionID(ctx, s.Main())
			if err != nil || idAfter != idBefore {
				t.Errorf("session changed: %s -> %s (%v)", idBefore, idAfter, err)
			}
			// Closing a tab (Result.Release) on an unread stream is fast too.
			res2, err := r.Grid(ctx, bigQuery(srv.name), nil)
			if err != nil {
				t.Fatal(err)
			}
			start = time.Now()
			res2.Release()
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("Release waited %v", d)
			}
			if _, err := r.Query(ctx, "SELECT 1"); err != nil {
				t.Fatalf("after Release: %v", err)
			}
		})
	}
}
