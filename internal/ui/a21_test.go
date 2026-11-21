package ui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"dbcc/internal/config"
	"dbcc/internal/dialect/mysql"
	"dbcc/internal/meta"
	"dbcc/internal/script"
	"dbcc/internal/session"
)

// TestA21ReplaceAlwaysWarnsOnMySQL: replacing a routine on MySQL (DROP +
// CREATE, drops privileges) always asks with the warning, also without
// confirm_writes; MariaDB (CREATE OR REPLACE) does not ask then.
func TestA21ReplaceAlwaysWarnsOnMySQL(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "6RunAll", 2*time.Second)
	ref := &script.ObjectRef{Path: meta.Path{Conn: "p", Database: "db", Object: "calc"}, Kind: meta.KindProcedure}
	tabc := make(chan any, 1)
	a.tv.QueueUpdate(func() {
		tabc <- a.editors.OpenObject("procedure:/p/db/calc", "calc", "CREATE PROCEDURE calc() SELECT 1", ref)
	})
	tab := (<-tabc)
	_ = tab

	run := func(d *mysql.MySQL) chan error {
		s := &session.Session{Profile: &config.Profile{Name: "p", Driver: "mysql"}, Dialect: d}
		done := make(chan error, 1)
		go func() {
			ch := make(chan error, 1)
			a.tv.QueueUpdate(func() {})
			t2 := a.editors.Current()
			go func() { ch <- a.confirmObjectReplace(s, t2) }()
			done <- <-ch
		}()
		return done
	}

	// MySQL: the dialog appears with the privilege warning; Yes proceeds.
	done := run(&mysql.MySQL{})
	waitFor(t, a, scr, "automatic_sp_privileges", 3*time.Second)
	scr.InjectKey(tcell.KeyEnter, 0, tcell.ModNone) // "Yes" has the focus
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MySQL replace after Yes: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answer after Yes")
	}

	// MariaDB without confirm_writes: no dialog at all.
	done = run(&mysql.MySQL{MariaDB: true})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MariaDB replace: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MariaDB asked although it keeps privileges and confirm_writes is off")
	}
}
