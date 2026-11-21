//go:build integration

package complete

// Integration test against real MySQL and MariaDB servers:
//
//	DBCC_IT_MYSQL=127.0.0.1:13306 DBCC_IT_MARIADB=127.0.0.1:13307 DBCC_IT_PW=<root password>
//	go test -tags integration -count=1 ./internal/ui/complete/

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"dbcc/internal/config"
	_ "dbcc/internal/dialect/mysql"
	"dbcc/internal/exec"
	"dbcc/internal/meta"
	"dbcc/internal/session"
)

const itDB = "dbcc_it_complete"

func TestIntegrationAliasResolution(t *testing.T) {
	servers := map[string]string{"mysql": os.Getenv("DBCC_IT_MYSQL"), "mariadb": os.Getenv("DBCC_IT_MARIADB")}
	ran := false
	for name, addr := range servers {
		if addr == "" {
			continue
		}
		ran = true
		t.Run(name, func(t *testing.T) { runAliasIT(t, addr) })
	}
	if !ran {
		t.Skip("no DBCC_IT_* servers configured")
	}
}

func runAliasIT(t *testing.T, addr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	host, ps, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(ps)
	p := &config.Profile{Name: "it", Driver: "mysql", Host: host, Port: port, User: "root"}
	cred := config.Credentials{Host: host, Port: port, User: "root", Password: os.Getenv("DBCC_IT_PW")}
	s, err := session.Open(ctx, p, cred, "")
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
	run("DROP DATABASE IF EXISTS " + itDB)
	run("CREATE DATABASE " + itDB)
	defer s.Main().ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+itDB)
	if err := s.ChangeDatabase(ctx, itDB); err != nil {
		t.Fatal(err)
	}
	run("CREATE TABLE customers (id INT PRIMARY KEY, name VARCHAR(40), `order` INT, `e mail` VARCHAR(80))")
	run("CREATE TABLE orders (id INT PRIMARY KEY, customer_id INT, total DECIMAL(10,2))")
	run("CREATE VIEW v_big AS SELECT id, total FROM orders WHERE total > 100")
	run("CREATE PROCEDURE do_thing() SELECT 1")

	loads := 0
	cache := meta.NewCache(func(conn string) (meta.Loader, bool) {
		if conn != "it" {
			return nil, false
		}
		loads++
		return SessionLoader(s), true
	})
	comp := New(cache, s.Dialect, s.Path())

	check := func(src string, want ...string) {
		t.Helper()
		text, cur := cursorAt(src)
		got := texts(comp.Candidates(ctx, text, cur))
		if !eq(got, want) {
			t.Errorf("%s\n got  %q\n want %q", src, got, want)
		}
	}
	check("SELECT c.| FROM customers c JOIN orders o ON o.customer_id = c.id", "id", "name", "`order`", "`e mail`")
	check("SELECT c.id, o.| FROM customers c JOIN orders o ON o.customer_id = c.id", "id", "customer_id", "total")
	check("SELECT * FROM "+itDB+".customers AS c WHERE c.na|", "name")
	check("SELECT * FROM CUSTOMERS AS c WHERE c.i|", "id")
	check("SELECT v.| FROM v_big v", "id", "total")
	check("SELECT * FROM "+itDB+".|", "customers", "orders", "v_big")
	check("SELECT * FROM o|", "orders")
	check("CALL d|", "do_thing")

	// DDL through the runner hook invalidates: a new column shows up.
	hook := DDLInvalidator(cache, s, nil)
	run("ALTER TABLE orders ADD COLUMN note TEXT")
	check("SELECT o.no| FROM orders o") // still cached: no "note"
	hook(exec.Message{Stmt: "ALTER TABLE orders ADD COLUMN note TEXT"})
	check("SELECT o.no| FROM orders o", "note")
	if loads == 0 {
		t.Fatal("loader never used")
	}
}
