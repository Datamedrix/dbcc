//go:build integration

package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

// TestPagingKeysEndToEnd: n, F6 and p page through a result in the grid
// (page_size 100); the status line shows the page.
func TestPagingKeysEndToEnd(t *testing.T) {
	for server, addr := range itServers(t) {
		t.Run(server, func(t *testing.T) {
			cfg := itProfile(t, "it", addr, nil)
			cfg.Settings.PageSize = 100
			a, scr, stop := startApp(t, cfg, Options{NoSplash: true, Profile: "it"})
			defer stop()
			waitFor(t, a, scr, "information_schema", 5*time.Second)
			q := "WITH d AS (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 " +
				"UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) " +
				"SELECT a.n*100 + b.n*10 + c.n + 1 AS n, CONCAT('v', a.n*100 + b.n*10 + c.n + 1) AS v " +
				"FROM d a, d b, d c WHERE a.n*100 + b.n*10 + c.n + 1 <= 250 ORDER BY n"
			keys(scr, tcell.KeyF9, q, tcell.KeyEnter)
			waitFor(t, a, scr, "rows 1–100 · page 1 +", 5*time.Second)
			a.tv.QueueUpdateDraw(func() { a.tv.SetFocus(a.results.Current().primitive()) })
			keys(scr, "n")
			lines := waitFor(t, a, scr, "rows 101–200 · page 2 +", 5*time.Second)
			if all := strings.Join(lines, "\n"); !strings.Contains(all, "v101") || strings.Contains(all, "v100 ") {
				t.Fatalf("page 2 not shown:\n%s", all)
			}
			keys(scr, tcell.KeyF6)
			waitFor(t, a, scr, "rows 201–250 · page 3", 5*time.Second)
			keys(scr, "n")
			waitFor(t, a, scr, "last page", 5*time.Second)
			keys(scr, "p")
			waitFor(t, a, scr, "rows 101–200 · page 2 +", 5*time.Second)
			keys(scr, "p")
			lines = waitFor(t, a, scr, "rows 1–100 · page 1 +", 5*time.Second)
			if all := strings.Join(lines, "\n"); !strings.Contains(all, "v1 ") {
				t.Fatalf("page 1 not shown:\n%s", all)
			}
		})
	}
}
