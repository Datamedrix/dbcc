//go:build integration

package complete

// Alias resolution on PostgreSQL (schema level, metadata through
// Session.AuxFor):
//
//	DBCC_IT_POSTGRES=127.0.0.1:15432 DBCC_IT_PG_PW=<postgres password>

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"dbcc/internal/config"
	_ "dbcc/internal/dialect/postgres"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/session"
)

func TestIntegrationAliasResolutionPostgres(t *testing.T) {
	addr := os.Getenv("DBCC_IT_POSTGRES")
	if addr == "" {
		t.Skip("DBCC_IT_POSTGRES not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	host, ps, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(ps)
	p := &config.Profile{Name: "it", Driver: "postgres", Host: host, Port: port, User: "postgres",
		Params: map[string]string{"sslmode": "disable"}}
	cred := config.Credentials{Host: host, Port: port, User: "postgres", Password: os.Getenv("DBCC_IT_PG_PW")}
	admin, err := session.Open(ctx, p, cred, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	drop := "DROP DATABASE IF EXISTS " + itDB + " WITH (FORCE)"
	for _, q := range []string{drop, "CREATE DATABASE " + itDB} {
		if _, err := admin.Main().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	defer admin.Main().ExecContext(context.Background(), drop)
	s, err := session.Open(ctx, p, cred, itDB)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := func(q string) {
		t.Helper()
		if _, err := s.Main().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	run(`CREATE TABLE customers (id int PRIMARY KEY, name varchar(40), "order" int, "e mail" varchar(80))`)
	run("CREATE TABLE orders (id int PRIMARY KEY, customer_id int, total numeric(10,2))")
	run("CREATE VIEW v_big AS SELECT id, total FROM orders WHERE total > 100")
	run("CREATE SCHEMA app")
	run("CREATE TABLE app.things (thing_id int, label text)")
	run("CREATE FUNCTION do_thing() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$")

	loads := 0
	cache := meta.NewCache(func(conn string) (meta.Loader, bool) {
		if conn != "it" {
			return nil, false
		}
		loads++
		return SessionLoader(s), true
	})
	if s.Path().Schema != "public" {
		t.Fatalf("path %v", s.Path())
	}
	comp := New(cache, s.Dialect, s.Path())
	check := func(src string, want ...string) {
		t.Helper()
		text, cur := cursorAt(src)
		got := texts(comp.Candidates(ctx, text, cur))
		if !eq(got, want) {
			t.Errorf("%s\n got  %q\n want %q", src, got, want)
		}
	}
	check("SELECT c.| FROM customers c JOIN orders o ON o.customer_id = c.id", "id", "name", `"order"`, `"e mail"`)
	check("SELECT c.id, o.| FROM customers c JOIN orders o ON o.customer_id = c.id", "id", "customer_id", "total")
	check("SELECT * FROM public.customers AS c WHERE c.na|", "name")
	check("SELECT t.| FROM app.things t", "thing_id", "label")
	check("SELECT v.| FROM v_big v", "id", "total")
	check("SELECT * FROM app.|", "things")
	check("SELECT * FROM o|", "orders")

	hook := DDLInvalidator(cache, s, nil)
	run("ALTER TABLE orders ADD COLUMN note text")
	check("SELECT o.no| FROM orders o")
	hook(exec.Message{Stmt: "ALTER TABLE orders ADD COLUMN note text"})
	check("SELECT o.no| FROM orders o", "note")
	if loads == 0 {
		t.Fatal("loader never used")
	}
}
