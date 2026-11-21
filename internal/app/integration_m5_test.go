//go:build integration

package app

// M4/M5 integration tests: every default Lua command headless against
// MySQL and MariaDB (the contract tests of the Lua API, spec 13, 16). The
// commands of M1 (cd, ls, pwd, ll, head, wc) are covered in
// integration_m1_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"dbcc/internal/command"
	"dbcc/internal/config"
	"dbcc/internal/history"
)

func TestMain(m *testing.M) {
	// Keep the statement history of the tests out of the owner's state dir.
	dir, err := os.MkdirTemp("", "dbcc-it-state")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const m5DB = "dbcc_it_m5"

const m5Data = `INSERT INTO parent (a, b, label) VALUES (1, 'x', 'one'), (2, 'y', NULL);
INSERT INTO items (code, region, name, no_default, price, qty, parent_a, parent_b, status)
VALUES ('A1', 'EU', 'Alpha', 'n', 2.50, 4, 1, 'x', 'active'),
       ('B2', 'US', 'Beta', 'n', 10.00, 1, 2, 'y', 'new'),
       ('C3', 'EU', 'Gamma', 'n', 1.25, 8, NULL, NULL, 'active');
`

// m5Setup creates the database with the rich schema of the MySQL dialect
// tests plus a few rows.
func m5Setup(t *testing.T, s server) *config.Config {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no user commands
	cfg := setupDB(t, s, m5DB)
	for _, f := range []string{"schema.sql", "schema_" + s.name + ".sql"} {
		b, err := os.ReadFile(filepath.Join("..", "dialect", "mysql", "testdata", f))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, stderr, code := run(t, cfg, Options{}, string(b)); code != 0 {
			t.Fatalf("schema %s: %s", f, stderr)
		}
	}
	if _, stderr, code := run(t, cfg, Options{}, m5Data); code != 0 {
		t.Fatalf("data: %s", stderr)
	}
	return cfg
}

// must runs a command line and fails on a non-zero exit.
func must(t *testing.T, cfg *config.Config, o Options) string {
	t.Helper()
	out, stderr, code := run(t, cfg, o, "")
	if code != 0 {
		t.Fatalf("%s: exit %d: %s", o.Command, code, stderr)
	}
	return out
}

// section returns the lines of a cat section (title to blank line).
func section(out, title string) string {
	i := strings.Index(out, "\n"+title+"\n")
	if i < 0 {
		return ""
	}
	rest := out[i+1:]
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestM5Cat(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := m5Setup(t, s)
			out := must(t, cfg, Options{Command: "cat items"})
			for _, w := range []string{"Columns\n", "fk_items_parent", "chk_price", "ix_price_desc", "trg_items_au", "Table options"} {
				if !strings.Contains(out, w) {
					t.Errorf("cat items lacks %q", w)
				}
			}
			if strings.Contains(out, "\x1b[") {
				t.Errorf("colour codes in headless output")
			}
			out = must(t, cfg, Options{Command: "cat -t procedure p_count"})
			if !strings.Contains(out, "CREATE") || !strings.Contains(out, "PROCEDURE `p_count`") {
				t.Errorf("cat procedure:\n%s", out)
			}
			out = must(t, cfg, Options{Command: "cat -t view /it/" + m5DB + "/v_items"})
			if !strings.Contains(out, "VIEW `v_items`") && !strings.Contains(out, "VIEW `"+m5DB+"`.`v_items`") {
				t.Errorf("cat view:\n%s", out)
			}
			for _, c := range []struct {
				cmd  string
				code int
				want string
			}{
				{"cat nosuch", ExitSQL, "no such object"},
				{"cat -t function p_count", ExitSQL, "no such object"},
				{"cat -t nosuchkind items", ExitSQL, "unknown object kind"},
				{"cat -x items", ExitUsage, "unknown flag -x"},
				{"cat", ExitUsage, "usage: cat [-t kind] <object>"},
			} {
				if _, stderr, code := run(t, cfg, Options{Command: c.cmd}, ""); code != c.code || !strings.Contains(stderr, c.want) {
					t.Errorf("%s: exit %d %q, want %d %q", c.cmd, code, stderr, c.code, c.want)
				}
			}
		})
	}
}

func TestM5Edit(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := m5Setup(t, s)
			script := must(t, cfg, Options{Command: "edit p_count"})
			if !strings.Contains(script, "DELIMITER") || !strings.Contains(script, "p_count") {
				t.Fatalf("edit p_count:\n%s", script)
			}
			want := "DROP PROCEDURE IF EXISTS"
			if s.name == "mariadb" {
				want = "CREATE OR REPLACE"
			}
			if !strings.Contains(script, want) {
				t.Errorf("replace script lacks %q:\n%s", want, script)
			}
			// The printed script replaces the object unchanged.
			changed := strings.Replace(script, "count items of a region", "count items per region", 1)
			if _, stderr, code := run(t, cfg, Options{}, changed); code != 0 {
				t.Fatalf("running the replace script: %s", stderr)
			}
			if out := must(t, cfg, Options{Command: "cat p_count"}); !strings.Contains(out, "count items per region") {
				t.Errorf("procedure not replaced:\n%s", out)
			}
			for _, cmd := range []string{"edit v_items", "edit f_double", "edit -t trigger trg_items_bi", "edit ev_cleanup"} {
				if out := must(t, cfg, Options{Command: cmd}); !strings.Contains(out, "CREATE") {
					t.Errorf("%s:\n%s", cmd, out)
				}
			}
			if _, stderr, code := run(t, cfg, Options{Command: "edit items"}, ""); code != ExitSQL || !strings.Contains(stderr, "no such object") {
				t.Errorf("edit table: %d %q", code, stderr)
			}
		})
	}
}

func TestM5CpMv(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := m5Setup(t, s)
			// Confirmation shows the SQL; headless needs --yes.
			_, stderr, code := run(t, cfg, Options{Command: "cp items items_d"}, "")
			if code != ExitSQL || !strings.Contains(stderr, "confirmation required") ||
				!strings.Contains(stderr, "CREATE TABLE `"+m5DB+"`.`items_d` AS SELECT * FROM `"+m5DB+"`.`items`") {
				t.Errorf("cp without --yes: %d %q", code, stderr)
			}
			_, stderr, code = run(t, cfg, Options{Command: "cp items items_d", Yes: true}, "")
			if code != 0 || !strings.Contains(stderr, "(3 rows)") {
				t.Fatalf("cp: %d %q", code, stderr)
			}
			if out := must(t, cfg, Options{Command: "wc items_d", Format: "csv"}); !strings.Contains(out, "3,items_d") {
				t.Errorf("cp data: %q", out)
			}

			// cp -a keeps keys, indexes, checks and generated columns (M5 acceptance).
			_, stderr, code = run(t, cfg, Options{Command: "cp -a items items_a", Yes: true}, "")
			if code != 0 || !strings.Contains(stderr, "(3 rows)") {
				t.Fatalf("cp -a: %d %q", code, stderr)
			}
			orig := must(t, cfg, Options{Command: "cat items"})
			cp := must(t, cfg, Options{Command: "cat items_a"})
			for _, sec := range []string{"Columns", "Primary and unique keys", "Indexes"} {
				if a, b := section(orig, sec), section(cp, sec); a == "" || a != b {
					t.Errorf("cp -a: section %s differs:\n%s\n--- copy\n%s", sec, a, b)
				}
			}
			exprs := regexp.MustCompile(`\| (\(?` + "`" + `(price|code|qty)` + "`" + `[^|]*)\|`)
			ce := func(out string) []string {
				var xs []string
				for _, m := range exprs.FindAllStringSubmatch(section(out, "Check constraints"), -1) {
					xs = append(xs, strings.TrimSpace(m[1]))
				}
				sort.Strings(xs)
				return xs
			}
			if a, b := ce(orig), ce(cp); len(a) != 3 || strings.Join(a, ";") != strings.Join(b, ";") {
				t.Errorf("cp -a checks: %v vs %v", a, b)
			}
			if !strings.Contains(section(cp, "Foreign keys"), "(none)") || !strings.Contains(section(cp, "Triggers"), "(none)") {
				t.Errorf("cp -a copied foreign keys or triggers:\n%s", cp)
			}
			q := "SELECT id, code, total, name_upper, uid FROM %s ORDER BY id"
			a := must(t, cfg, Options{Command: strings.Replace(q, "%s", "items", 1), Format: "csv"})
			b := must(t, cfg, Options{Command: strings.Replace(q, "%s", "items_a", 1), Format: "csv"})
			if a != b || !strings.Contains(a, "10.00,ALPHA") && !strings.Contains(a, "10.00,ALPHA") {
				t.Errorf("cp -a data (generated columns recomputed):\n%s\n%s", a, b)
			}
			// A partitioned table and a view.
			must(t, cfg, Options{Command: "cp -a logs logs_a", Yes: true})
			if out := must(t, cfg, Options{Command: "cat logs_a"}); !strings.Contains(out, "p2023, p2024, pmax") {
				t.Errorf("cp -a partitioning:\n%s", out)
			}
			must(t, cfg, Options{Command: "cp -a v_items v_copy", Yes: true})
			if out := must(t, cfg, Options{Command: "ll v_copy", Format: "csv"}); len(csvLines(out)) != 3 {
				t.Errorf("view copy: %q", out)
			}
			if _, stderr, code := run(t, cfg, Options{Command: "cp items items_d", Yes: true}, ""); code != ExitSQL || !strings.Contains(stderr, "already exists") {
				t.Errorf("cp onto an existing table: %d %q", code, stderr)
			}
			if _, stderr, code := run(t, cfg, Options{Command: "cp p_count x", Yes: true}, ""); code != ExitSQL || !strings.Contains(stderr, "not a table or view") {
				t.Errorf("cp procedure: %d %q", code, stderr)
			}

			// mv renames tables and views.
			if _, stderr, code := run(t, cfg, Options{Command: "mv items_d items_e"}, ""); code != ExitSQL || !strings.Contains(stderr, "RENAME TABLE") {
				t.Errorf("mv without --yes: %d %q", code, stderr)
			}
			must(t, cfg, Options{Command: "mv items_d items_e", Yes: true})
			must(t, cfg, Options{Command: "mv v_copy v_moved", Yes: true})
			out := must(t, cfg, Options{Command: "ls", Format: "csv"})
			if !strings.Contains(out, "items_e,table") || strings.Contains(out, "items_d,") || !strings.Contains(out, "v_moved,view") {
				t.Errorf("after mv:\n%s", out)
			}
			for _, c := range []string{"mv p_count p2", "mv items_e other.x", "mv nosuch x"} {
				if _, _, code := run(t, cfg, Options{Command: c, Yes: true}, ""); code != ExitSQL {
					t.Errorf("%s: exit %d", c, code)
				}
			}
			// Write commands are blocked in read-only profiles.
			ro := multiConfigDB(t, s, m5DB, "read_only = true")
			if _, stderr, code := run(t, ro, Options{Command: "cp items items_ro", Yes: true}, ""); code != ExitSQL || !strings.Contains(stderr, "read-only") {
				t.Errorf("cp read-only: %d %q", code, stderr)
			}
		})
	}
}

// multiConfigDB is multiConfig for another database.
func multiConfigDB(t *testing.T, s server, db, extra string) *config.Config {
	t.Helper()
	cfg := multiConfig(t, s, extra)
	for _, p := range cfg.Profiles {
		p.Database = db
	}
	return cfg
}

func TestM5DuGrepPs(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := m5Setup(t, s)
			out := must(t, cfg, Options{Command: "du", Format: "csv"})
			lines := csvLines(out)
			if lines[0] != "table,engine,rows (estimate),data,index,total,free,total bytes" || len(lines) != 5 ||
				!strings.Contains(out, "items,InnoDB,") || !strings.Contains(out, "(total 3 tables)") || strings.Contains(out, "v_items") {
				t.Errorf("du:\n%s", out)
			}
			out = must(t, cfg, Options{Command: "du items", Format: "csv"})
			if n := len(csvLines(out)); n != 2 || !strings.Contains(out, "KiB") {
				t.Errorf("du items:\n%s", out)
			}
			if _, stderr, code := run(t, cfg, Options{Command: "du v_items"}, ""); code != ExitSQL || !strings.Contains(stderr, "is a view") {
				t.Errorf("du view: %d %q", code, stderr)
			}

			out = must(t, cfg, Options{Command: "grep qty", Format: "csv"})
			for _, w := range []string{"items,column,column,qty int", "v_items,view,source,"} {
				if !strings.Contains(out, w) {
					t.Errorf("grep qty lacks %q:\n%s", w, out)
				}
			}
			out = must(t, cfg, Options{Command: "grep P_REGION", Format: "csv"})
			if !strings.Contains(out, "p_count,procedure,source,line 3: SELECT COUNT(*) INTO n FROM items WHERE region = p_region;") {
				t.Errorf("grep source line:\n%s", out)
			}
			out = must(t, cfg, Options{Command: "grep double", Format: "csv"})
			if !strings.Contains(out, "f_double,function,name,") {
				t.Errorf("grep names:\n%s", out)
			}
			out = must(t, cfg, Options{Command: "grep RATI", Format: "csv"})
			if !strings.Contains(out, "items,column,column,ratio double") {
				t.Errorf("grep names:\n%s", out)
			}
			out = must(t, cfg, Options{Command: "grep cleanup /it/" + m5DB, Format: "csv"})
			if !strings.Contains(out, "ev_cleanup,event,name,") {
				t.Errorf("grep event:\n%s", out)
			}
			out = must(t, cfg, Options{Command: "grep trg_", Format: "csv"})
			if strings.Count(out, ",trigger,name,") != 2 {
				t.Errorf("grep triggers:\n%s", out)
			}
			if out := must(t, cfg, Options{Command: "grep zzz_nothing", Format: "csv"}); len(csvLines(out)) != 1 {
				t.Errorf("grep without match:\n%s", out)
			}

			out = must(t, cfg, Options{Command: "ps", Format: "csv"})
			if !strings.HasPrefix(out, "me,id,user,host,db,command,time,state,info\n") || !strings.Contains(out, "\n*,") {
				t.Errorf("ps:\n%s", out)
			}
		})
	}
}

func TestM5Kill(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			m5Setup(t, s)
			cfg := multiConfigDB(t, s, m5DB, "")
			var out, errb bytes.Buffer
			env := &Env{Opts: &Options{Profile: "it", Format: "csv", Yes: true}, Config: cfg, Stdout: &out, Stderr: &errb}
			h := newHeadless(env)
			defer h.ws.CloseAll()
			ctx := context.Background()
			if _, err := h.ws.Chdir(ctx, "/it/"+m5DB); err != nil {
				t.Fatal(err)
			}
			reg, err := h.registry()
			if err != nil {
				t.Fatal(err)
			}
			defer reg.Runtime().Close()
			victim, err := h.ws.Open(ctx, "it2")
			if err != nil {
				t.Fatal(err)
			}
			var id string
			if err := victim.Main().QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
				t.Fatal(err)
			}
			var me string
			h.ws.Current().Main().QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&me)
			if err := reg.Dispatch(ctx, "kill "+me); err == nil || !strings.Contains(err.Error(), "is this session") {
				t.Errorf("kill own session: %v", err)
			}
			if err := reg.Dispatch(ctx, "kill -q "+id); err != nil {
				t.Fatalf("kill -q: %v", err)
			}
			if err := victim.Main().PingContext(ctx); err != nil {
				t.Errorf("kill -q terminated the session: %v", err)
			}
			if err := reg.Dispatch(ctx, "kill "+id); err != nil {
				t.Fatalf("kill: %v", err)
			}
			if !strings.Contains(errb.String(), "kill: session "+id+" terminated") {
				t.Errorf("kill message: %q", errb.String())
			}
			var one int
			if err := victim.Main().QueryRowContext(ctx, "SELECT 1").Scan(&one); err == nil {
				t.Errorf("killed session still answers")
			}
			if err := reg.Dispatch(ctx, "kill x"); err == nil || !strings.Contains(err.Error(), "must be an integer") {
				t.Errorf("kill x: %v", err)
			}
		})
	}
	// Without --yes the confirmation shows the statement.
	for _, s := range servers(t) {
		cfg := multiConfigDB(t, s, "", "")
		if _, stderr, code := run(t, cfg, Options{Command: "kill 999999999"}, ""); code != ExitSQL || !strings.Contains(stderr, "KILL 999999999") {
			t.Errorf("%s: kill without --yes: %d %q", s.name, code, stderr)
		}
	}
}

func TestM5Export(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := m5Setup(t, s)
			var out, errb bytes.Buffer
			env := &Env{Opts: &Options{Profile: "it", Format: "csv"}, Config: cfg, Stdout: &out, Stderr: &errb}
			h := newHeadless(env)
			defer h.ws.CloseAll()
			ctx := context.Background()
			if _, err := h.ws.Chdir(ctx, "/it/"+m5DB); err != nil {
				t.Fatal(err)
			}
			reg, err := h.registry()
			if err != nil {
				t.Fatal(err)
			}
			defer reg.Runtime().Close()
			dir := t.TempDir()
			csvPath := filepath.Join(dir, "items.csv")
			if err := reg.Dispatch(ctx, "export "+csvPath); err == nil || !strings.Contains(err.Error(), "no active result") {
				t.Errorf("export without result: %v", err)
			}
			if err := reg.Dispatch(ctx, "SELECT id, code, label FROM items LEFT JOIN parent ON a = parent_a ORDER BY id"); err != nil {
				t.Fatal(err)
			}
			if err := reg.Dispatch(ctx, "export "+csvPath); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(csvPath)
			if string(b) != "id,code,label\n1,A1,one\n2,B2,\n3,C3,\n" {
				t.Errorf("csv:\n%s", b)
			}
			if !strings.Contains(errb.String(), "export: 3 rows written to "+csvPath) {
				t.Errorf("message: %q", errb.String())
			}
			if err := reg.Dispatch(ctx, "export "+csvPath); err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Errorf("overwrite: %v", err)
			}
			if err := reg.Dispatch(ctx, "ll parent"); err != nil {
				t.Fatal(err)
			}
			if err := reg.Dispatch(ctx, "export -f "+csvPath); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(csvPath); !strings.HasPrefix(string(b), "a,b,label\n") {
				t.Errorf("forced overwrite:\n%s", b)
			}
			jsonPath := filepath.Join(dir, "parent.json")
			if err := reg.Dispatch(ctx, "export "+jsonPath); err != nil {
				t.Fatal(err)
			}
			var rows []map[string]any
			b, _ = os.ReadFile(jsonPath)
			if err := json.Unmarshal(b, &rows); err != nil || len(rows) != 2 || rows[1]["label"] != nil || rows[0]["b"] != "x" {
				t.Errorf("json %v:\n%s", err, b)
			}
			if err := reg.Dispatch(ctx, "export "+filepath.Join(dir, "x.xlsx")); err == nil {
				t.Errorf("unknown format accepted")
			}
		})
	}
}

func TestM5History(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := m5Setup(t, s)
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			must(t, cfg, Options{Command: "SELECT 42 AS answer"})
			run(t, cfg, Options{Command: "SELECT nosuchcolumn FROM items"}, "")
			must(t, cfg, Options{Command: "SELECT 'my PASSWORD' AS secret"})
			run(t, cfg, Options{Command: "CREATE USER IF NOT EXISTS dbcc_hist_u IDENTIFIED BY 'pw1'; DROP USER IF EXISTS dbcc_hist_u"}, "")
			must(t, cfg, Options{Command: "ll parent"})
			out := must(t, cfg, Options{Command: "history", Format: "csv"})
			lines := csvLines(out)
			if lines[0] != "#,time,profile,path,ms,outcome,statement" {
				t.Fatalf("history header: %s", out)
			}
			body := strings.Join(lines[1:], "\n")
			for _, w := range []string{",it,/it/" + m5DB + ",", ",ok,SELECT 42 AS answer", "Unknown column", "SELECT nosuchcolumn FROM items",
				"DROP USER IF EXISTS dbcc_hist_u", "SELECT * FROM `" + m5DB + "`.`parent`"} {
				if !strings.Contains(body, w) {
					t.Errorf("history lacks %q:\n%s", w, out)
				}
			}
			for _, bad := range []string{"PASSWORD", "IDENTIFIED", "pw1"} {
				if strings.Contains(body, bad) {
					t.Errorf("history records %q:\n%s", bad, out)
				}
			}
			out = must(t, cfg, Options{Command: "history 2", Format: "csv"})
			if lines := csvLines(out); len(lines) != 3 || !strings.Contains(lines[2], "parent") {
				t.Errorf("history 2:\n%s", out)
			}
			p := filepath.Join(state, "dbcc", history.FileName)
			if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
				t.Errorf("history file %s: %v %v", p, st, err)
			}
			if _, stderr, code := run(t, cfg, Options{Command: "history 0"}, ""); code != ExitSQL || !strings.Contains(stderr, "positive") {
				t.Errorf("history 0: %d %q", code, stderr)
			}
		})
	}
}

func TestM5Help(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := itConfig(t, s, "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			out := must(t, cfg, Options{Command: "help", Format: "csv"})
			names, _ := command.DefaultScripts()
			for _, n := range names {
				if !strings.Contains(out, "\n"+strings.TrimSuffix(n, ".lua")+",") {
					t.Errorf("help lacks %s:\n%s", n, out)
				}
			}
			out = must(t, cfg, Options{Command: "help cp"})
			for _, w := range []string{"usage: cp [-a] <src> <new>", "flags:", "  -a", "writes", "embedded:cp.lua"} {
				if !strings.Contains(out, w) {
					t.Errorf("help cp lacks %q:\n%s", w, out)
				}
			}
			if out := must(t, cfg, Options{Command: "help cat"}); !strings.Contains(out, "-t <string>") {
				t.Errorf("help cat:\n%s", out)
			}
			if out := must(t, cfg, Options{Command: "help about"}); !strings.Contains(out, "dbcc") || !strings.Contains(out, "Datamedrix") {
				t.Errorf("help about:\n%s", out)
			}
			if _, stderr, code := run(t, cfg, Options{Command: "help nosuch"}, ""); code != ExitSQL || !strings.Contains(stderr, "no command nosuch") {
				t.Errorf("help nosuch: %d %q", code, stderr)
			}
		})
	}
}

// TestEveryDefaultCommandIsTested keeps the contract-test list complete:
// a new default command needs a headless integration test.
func TestEveryDefaultCommandIsTested(t *testing.T) {
	tested := map[string]string{
		"cd": "TestM1CdGoAPI", "ls": "TestM1Ls", "pwd": "TestM1CdGoAPI", "ll": "TestM1DataCommands",
		"head": "TestM1DataCommands", "wc": "TestM1DataCommands", "cat": "TestM5Cat", "edit": "TestM5Edit",
		"cp": "TestM5CpMv", "mv": "TestM5CpMv", "du": "TestM5DuGrepPs", "grep": "TestM5DuGrepPs",
		"ps": "TestM5DuGrepPs", "kill": "TestM5Kill", "export": "TestM5Export", "history": "TestM5History",
		"help": "TestM5Help", "tail": "TestTailAllServers", "reload": "TestReloadHeadlessAllServers",
	}
	names, err := command.DefaultScripts()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, ok := tested[strings.TrimSuffix(n, ".lua")]; !ok {
			t.Errorf("default command %s has no headless integration test", n)
		}
	}
}
