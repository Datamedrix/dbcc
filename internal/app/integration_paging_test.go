//go:build integration

package app

// Result paging (spec 10.2, A24) on every server: forward from the open
// cursor, backward by re-running and skipping, paging after the open-cursor
// rule closed the cursor, last-page detection, arguments kept for re-runs,
// cancel while skipping, and export of the whole result.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dbcc/internal/exec"
)

// numbersQuery returns rows (n, v) for n = 1..10^digits in order of n,
// v = 'v' || n, restricted by "n <= limit" (limit is a placeholder or a
// literal).
func numbersQuery(server string, digits int, limit string) string {
	return genQuery(server, digits, limit, true)
}

// genQuery is numbersQuery, optionally without ORDER BY (then the rows
// stream without a sort on every server).
func genQuery(server string, digits int, limit string, ordered bool) string {
	from := ""
	expr := ""
	for i := 0; i < digits; i++ {
		alias := "d" + strconv.Itoa(i)
		if i > 0 {
			from += ", "
			expr += " + "
		}
		from += "d " + alias
		expr += alias + ".n * " + strconv.Itoa(pow10(digits-1-i))
	}
	expr += " + 1"
	order := " ORDER BY n"
	if !ordered {
		order = ""
		if server == "postgres" {
			server = "mssql" // the VALUES form streams; generate_series materializes
		}
	}
	switch server {
	case "postgres":
		return "SELECT g AS n, 'v' || g AS v FROM generate_series(1, " + strconv.Itoa(pow10(digits)) + ") g WHERE g <= " +
			limit + " ORDER BY g"
	case "mssql":
		return "WITH d AS (SELECT n FROM (VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)) v(n)), " +
			"t AS (SELECT " + expr + " AS n FROM " + from + ") SELECT n, CONCAT('v', n) AS v FROM t WHERE n <= " + limit + order
	default:
		return "WITH d AS (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 " +
			"UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9), " +
			"t AS (SELECT " + expr + " AS n FROM " + from + ") SELECT n, CONCAT('v', n) AS v FROM t WHERE n <= " + limit + order
	}
}

func pow10(n int) int {
	p := 1
	for ; n > 0; n-- {
		p *= 10
	}
	return p
}

// checkPage loads the rest of the page and checks its first and last n.
func checkPage(t *testing.T, res *exec.Result, page, first, last int) {
	t.Helper()
	if err := res.FetchAll(500); err != nil {
		t.Fatal(err)
	}
	rows := res.Rows()
	if res.Page() != page || len(rows) != last-first+1 || res.Offset() != first-1 {
		t.Fatalf("page %d with %d rows at offset %d; want page %d rows %d..%d", res.Page(), len(rows), res.Offset(), page, first, last)
	}
	if rows[0][0] != strconv.Itoa(first) || rows[len(rows)-1][0] != strconv.Itoa(last) || rows[0][1] != "v"+strconv.Itoa(first) {
		t.Fatalf("page %d: first %v last %v; want %d..%d", page, rows[0], rows[len(rows)-1], first, last)
	}
}

func TestPagingEveryServer(t *testing.T) {
	for _, srv := range allServers(t) {
		t.Run(srv.name, func(t *testing.T) {
			srv.prepare(t)
			s := openIt(t, srv.cfg(t, srv.db, ""), "it")
			ctx := context.Background()
			r := exec.NewRunner(s, &countSink{})
			r.FetchSize, r.PageSize = 500, 3000
			res, err := r.Grid(ctx, numbersQuery(srv.name, 4, s.Dialect.Placeholder(1)), nil, 7500)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Args) != 1 || res.Args[0] != 7500 {
				t.Fatalf("args not kept: %v", res.Args)
			}
			checkPage(t, res, 1, 1, 3000)
			if !res.HasNextPage() || res.More() || res.Cursor() == nil || res.Cursor().Done() {
				t.Fatalf("full page: next %v more %v", res.HasNextPage(), res.More())
			}
			if st := res.Status(); st != "rows 1–3,000 · page 1 +" {
				t.Errorf("status %q", st)
			}
			// Forward from the open cursor.
			if err := res.NextPage(ctx); err != nil {
				t.Fatal(err)
			}
			checkPage(t, res, 2, 3001, 6000)
			if err := res.NextPage(ctx); err != nil {
				t.Fatal(err)
			}
			checkPage(t, res, 3, 6001, 7500)
			if res.HasNextPage() {
				t.Fatal("last page reports a next page")
			}
			if err := res.NextPage(ctx); !errors.Is(err, exec.ErrLastPage) {
				t.Fatalf("beyond the last page: %v", err)
			}
			if st := res.Status(); st != "rows 6,001–7,500 · page 3" {
				t.Errorf("status %q", st)
			}
			// Backward: re-run with the same argument and skip.
			if err := res.PrevPage(ctx); err != nil {
				t.Fatal(err)
			}
			checkPage(t, res, 2, 3001, 6000)
			if err := res.PrevPage(ctx); err != nil {
				t.Fatal(err)
			}
			checkPage(t, res, 1, 1, 3000)
			if err := res.PrevPage(ctx); !errors.Is(err, exec.ErrFirstPage) {
				t.Fatalf("before the first page: %v", err)
			}
			// A new statement closes the cursor (open-cursor rule): the page
			// stays, marked partial; the next page re-runs and skips.
			if err := res.NextPage(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Query(ctx, "SELECT 1"); err != nil {
				t.Fatal(err)
			}
			if !res.Partial || !res.HasNextPage() || res.Len() != 500 {
				t.Fatalf("after a new statement: partial %v next %v len %d", res.Partial, res.HasNextPage(), res.Len())
			}
			if !strings.HasSuffix(res.Status(), "· partial") {
				t.Errorf("status %q", res.Status())
			}
			if n := res.SkipRows(true); n != 6000 {
				t.Errorf("SkipRows = %d", n)
			}
			if err := res.NextPage(ctx); err != nil {
				t.Fatal(err)
			}
			checkPage(t, res, 3, 6001, 7500)
			if res.Partial {
				t.Error("still partial after the re-run")
			}
			// The session is usable and the result releases cleanly.
			res.Release()
			if _, err := r.Query(ctx, "SELECT 1"); err != nil {
				t.Fatal(err)
			}
			// A page smaller than the fetch chunk: the first fetch fills the
			// page, the cursor stays open and holds the session (the runner
			// must not query the session behind it: that hung).
			sink := &countSink{}
			r.Sink, r.PageSize = sink, 100
			done := make(chan error, 1)
			go func() { done <- r.RunScript(ctx, numbersQuery(srv.name, 3, "250"), 0) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("statement with a page smaller than fetch_size hangs")
			}
			res = sink.results[len(sink.results)-1]
			checkPage(t, res, 1, 1, 100)
			if res.Cursor() == nil || res.Cursor().Done() {
				t.Fatal("cursor closed after the first full page")
			}
			if err := res.NextPage(ctx); err != nil {
				t.Fatal(err)
			}
			checkPage(t, res, 2, 101, 200)
			if err := res.PrevPage(ctx); err != nil {
				t.Fatal(err)
			}
			checkPage(t, res, 1, 1, 100)
			if _, err := r.Query(ctx, "SELECT 1"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestPagingCancelWhileSkipping: the session cancel (F8) stops a re-run
// that skips millions of rows; the page stays and the session survives.
func TestPagingCancelWhileSkipping(t *testing.T) {
	for _, srv := range allServers(t) {
		t.Run(srv.name, func(t *testing.T) {
			srv.prepare(t)
			s := openIt(t, srv.cfg(t, srv.db, ""), "it")
			ctx := context.Background()
			r := exec.NewRunner(s, &countSink{})
			r.FetchSize, r.PageSize = 500, 5000000
			res, err := r.Grid(ctx, genQuery(srv.name, 7, "10000000", false), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Query(ctx, "SELECT 1"); err != nil { // closes the cursor
				t.Fatal(err)
			}
			if !res.HasNextPage() || res.SkipRows(true) != 5000000 {
				t.Fatalf("next %v skip %d", res.HasNextPage(), res.SkipRows(true))
			}
			go func() {
				time.Sleep(300 * time.Millisecond)
				if err := s.Cancel(context.Background()); err != nil {
					t.Errorf("cancel: %v", err)
				}
			}()
			start := time.Now()
			err = res.NextPage(ctx)
			d := time.Since(start)
			if err == nil || errors.Is(err, exec.ErrLastPage) {
				t.Fatalf("skip was not cancelled after %v: %v", d, err)
			}
			t.Logf("cancelled after %v: %v", d, err)
			if d > 15*time.Second {
				t.Errorf("cancel took %v", d)
			}
			if res.Page() != 1 || res.Len() != 500 {
				t.Errorf("page changed: %d, %d rows", res.Page(), res.Len())
			}
			if _, err := r.Query(ctx, "SELECT 1"); err != nil {
				t.Fatalf("session after cancel: %v", err)
			}
		})
	}
}

// TestExportWholePagedResult: export writes all 10,000 rows of a result
// shown in pages of 3,000 — from page 1 (the open cursor continues) and
// from page 2 (the statement is re-run from the start).
func TestExportWholePagedResult(t *testing.T) {
	for _, srv := range allServers(t) {
		t.Run(srv.name, func(t *testing.T) {
			srv.prepare(t)
			x := newDX(t, srv.cfg(t, srv.db, ""), false)
			x.h.runner(x.h.ws.Current()).PageSize = 3000
			x.do(numbersQuery(srv.name, 4, "10000"))
			res := x.h.sink.last
			if res == nil || res.Len() != 3000 || !res.HasNextPage() {
				t.Fatalf("paged result expected: %v", res)
			}
			dir := t.TempDir()
			check := func(path string) {
				t.Helper()
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
				if len(lines) != 10001 || lines[0] != "n,v" {
					t.Fatalf("%s: %d lines, header %q", path, len(lines), lines[0])
				}
				for i, l := range lines[1:] {
					if want := strconv.Itoa(i+1) + ",v" + strconv.Itoa(i+1); l != want {
						t.Fatalf("%s line %d = %q, want %q", path, i+2, l, want)
					}
				}
			}
			p1 := filepath.Join(dir, "page1.csv")
			x.do("export " + p1)
			check(p1)
			if err := res.NextPage(context.Background()); err != nil {
				t.Fatal(err)
			}
			if res.Page() != 2 || res.Rows()[0][0] != "3001" {
				t.Fatalf("page %d starts with %v", res.Page(), res.Rows()[0])
			}
			p2 := filepath.Join(dir, "page2.json")
			x.do("export " + p2)
			b, _ := os.ReadFile(p2)
			if n := strings.Count(string(b), `"v":`); n != 10000 {
				t.Fatalf("json export from page 2: %d rows", n)
			}
			p3 := filepath.Join(dir, "page2.csv")
			x.do("export " + p3)
			check(p3)
			if res.Page() != 2 || res.Len() == 0 {
				t.Errorf("the tab's page changed: %d, %d rows", res.Page(), res.Len())
			}
		})
	}
}
