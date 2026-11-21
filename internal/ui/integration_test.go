//go:build integration

package ui

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/config"
	"dbcc/internal/session"

	_ "dbcc/internal/dialect/mysql"
)

func itServers(t *testing.T) map[string]string {
	out := map[string]string{}
	for name, env := range map[string]string{"mysql": "DBCC_IT_MYSQL", "mariadb": "DBCC_IT_MARIADB"} {
		if v := os.Getenv(env); v != "" {
			out[name] = v
		}
	}
	if len(out) == 0 {
		t.Skip("no DBCC_IT_* servers")
	}
	return out
}

func itProfile(t *testing.T, name, addr string, mod func(p *config.Profile)) *config.Config {
	host, port, _ := net.SplitHostPort(addr)
	pn, _ := strconv.Atoi(port)
	p := &config.Profile{Name: name, Driver: "mysql", Host: host, Port: pn, User: "root", Password: "env:DBCC_IT_PW"}
	if mod != nil {
		mod(p)
	}
	cfg := emptyConfig()
	cfg.Profiles[name] = p
	return cfg
}

// seed creates database dbccui with a table of long rows.
func seed(t *testing.T, cfg *config.Config, name string) {
	ws := session.NewWorkspace(cfg, nil)
	defer ws.CloseAll()
	s, err := ws.Open(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP DATABASE IF EXISTS dbccui", "CREATE DATABASE dbccui",
		"CREATE TABLE dbccui.items (id INT PRIMARY KEY, label VARCHAR(20), note TEXT)",
		"INSERT INTO dbccui.items VALUES (1,'first',REPEAT('x',500)),(2,'second',NULL)",
		"CREATE TABLE dbccui.log (id INT PRIMARY KEY)",
	} {
		if _, err := s.Main().ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		ws := session.NewWorkspace(cfg, nil)
		defer ws.CloseAll()
		if s, err := ws.Open(context.Background(), name); err == nil {
			s.Main().ExecContext(context.Background(), "DROP DATABASE IF EXISTS dbccui")
		}
	})
}

func keys(s tcell.SimulationScreen, ks ...any) {
	for _, k := range ks {
		switch v := k.(type) {
		case tcell.Key:
			s.InjectKey(v, 0, tcell.ModNone)
		case string:
			for _, r := range v {
				s.InjectKey(tcell.KeyRune, r, tcell.ModNone)
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func TestM2TreeOpensTableData(t *testing.T) {
	for server, addr := range itServers(t) {
		t.Run(server, func(t *testing.T) {
			cfg := itProfile(t, "it", addr, nil)
			seed(t, cfg, "it")
			a, scr, stop := startApp(t, cfg, Options{NoSplash: true, Profile: "it"})
			defer stop()
			lines := waitFor(t, a, scr, "information_schema", 5*time.Second)
			all := strings.Join(lines, "\n")
			if !strings.Contains(all, "dbccui") || strings.Index(all, "dbccui") > strings.Index(all, "information_schema") {
				t.Fatalf("user databases must come before system databases:\n%s", all)
			}
			// Focus the tree, select dbccui, expand it, open Tables, open items.
			keys(scr, tcell.KeyBacktab)
			a.tv.QueueUpdateDraw(func() {
				a.tree.SelectPath(a.ws.Path())
				for _, n := range a.tree.GetRoot().GetChildren() {
					if n.GetText() == "dbccui" {
						a.tree.SetCurrentNode(n)
					}
				}
			})
			keys(scr, tcell.KeyRight)
			waitFor(t, a, scr, "Tables (2)", 5*time.Second)
			keys(scr, tcell.KeyDown, tcell.KeyRight, tcell.KeyDown)
			keys(scr, tcell.KeyEnter)
			lines = waitFor(t, a, scr, "second", 5*time.Second)
			all = strings.Join(lines, "\n")
			for _, want := range []string{"label", "first", "NULL", "2 rows"} {
				if !strings.Contains(all, want) {
					t.Errorf("grid lacks %q:\n%s", want, all)
				}
			}
			// No cell ever wraps: the 500-char note occupies one screen line.
			n := 0
			for _, l := range lines {
				if strings.Contains(l, "xxxxxxxx") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("long value spans %d lines, want 1", n)
			}
		})
	}
}

func TestM8ManualTransactionAndReadOnly(t *testing.T) {
	for server, addr := range itServers(t) {
		t.Run(server, func(t *testing.T) {
			off := false
			seed(t, itProfile(t, "it", addr, nil), "it")
			cfg := itProfile(t, "it", addr, func(p *config.Profile) { p.Autocommit = &off; p.Database = "dbccui" })
			ro := itProfile(t, "ro", addr, func(p *config.Profile) { p.ReadOnly = true; p.Database = "dbccui" })
			cfg.Profiles["ro"] = ro.Profiles["ro"]

			a, scr, stop := startApp(t, cfg, Options{NoSplash: true, Profile: "it"})
			defer stop()
			waitFor(t, a, scr, "manual txn", 5*time.Second)
			keys(scr, tcell.KeyF9, "INSERT INTO log VALUES (1)", tcell.KeyEnter)
			waitFor(t, a, scr, "TXN OPEN", 5*time.Second)
			// Navigation to another profile keeps the transaction open.
			keys(scr, tcell.KeyF9, "cd /ro", tcell.KeyEnter)
			waitFor(t, a, scr, "READ ONLY", 5*time.Second)
			keys(scr, tcell.KeyF9, "INSERT INTO log VALUES (2)", tcell.KeyEnter)
			waitFor(t, a, scr, "read-only profile", 5*time.Second)
			keys(scr, tcell.KeyF9, "cd /it/dbccui", tcell.KeyEnter)
			waitFor(t, a, scr, "TXN OPEN", 5*time.Second)
			// cd to another database asks: commit, roll back or stay.
			keys(scr, tcell.KeyF9, "cd /it/mysql", tcell.KeyEnter)
			waitFor(t, a, scr, "Roll back", 5*time.Second)
			keys(scr, tcell.KeyRight, tcell.KeyEnter) // "Roll back"
			waitFor(t, a, scr, "/it/mysql >", 5*time.Second)
			var count string
			done := make(chan struct{})
			a.do(func() {
				s := a.ws.Current()
				row := s.Main().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM dbccui.log")
				var n int
				row.Scan(&n)
				count = fmt.Sprint(n)
				close(done)
			})
			<-done
			if count != "0" {
				t.Errorf("rollback did not happen: %s rows", count)
			}
		})
	}
}

// TestWhereCompletion: in the free WHERE prompt (w) Ctrl+Space completes
// columns of the table; the list never opens by itself.
func TestWhereCompletion(t *testing.T) {
	for server, addr := range itServers(t) {
		t.Run(server, func(t *testing.T) {
			cfg := itProfile(t, "it", addr, nil)
			seed(t, cfg, "it")
			a, scr, stop := startApp(t, cfg, Options{NoSplash: true, Profile: "it"})
			defer stop()
			waitFor(t, a, scr, "information_schema", 5*time.Second)
			keys(scr, tcell.KeyF9, "ll dbccui.items", tcell.KeyEnter)
			waitFor(t, a, scr, "second", 5*time.Second)
			keys(scr, "w", "la")
			lines := waitFor(t, a, scr, "WHERE la", 3*time.Second)
			if strings.Contains(strings.Join(lines, "\n"), "label") {
				t.Fatal("completion list opened without Ctrl+Space")
			}
			scr.InjectKey(tcell.KeyCtrlSpace, 0, tcell.ModCtrl)
			waitFor(t, a, scr, "label", 3*time.Second)
			keys(scr, tcell.KeyEnter) // accept
			waitFor(t, a, scr, "WHERE label", 3*time.Second)
			keys(scr, " = 'first'", tcell.KeyEnter) // apply
			lines = waitFor(t, a, scr, "filter:", 5*time.Second)
			if all := strings.Join(lines, "\n"); !strings.Contains(all, "first") || strings.Contains(all, "second") {
				t.Fatalf("WHERE not applied:\n%s", all)
			}
		})
	}
}
