//go:build integration

package app

// M1 integration tests: the default Lua commands headless against MySQL and
// MariaDB (the contract tests of the Lua API, spec 13, 16).

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbcc/internal/config"
)

const m1DB = "dbcc_it_m1"

const m1Schema = `CREATE TABLE customers (id INT PRIMARY KEY, name VARCHAR(20), city VARCHAR(20));
INSERT INTO customers VALUES (1,'a','x'),(2,'b','x'),(3,'c',NULL),(4,'d','y'),(5,'e','y'),(6,'f','y'),
  (7,'g','z'),(8,'h','z'),(9,'i','z'),(10,'j','z'),(11,'k','z'),(12,'l','z');
CREATE VIEW v_city AS SELECT DISTINCT city FROM customers WHERE city IS NOT NULL;
CREATE PROCEDURE p_noop() SELECT 1;
`

// m1Setup creates the test database with m1Schema and isolates the user
// command directory in a temporary XDG_CONFIG_HOME, which it returns.
func m1Setup(t *testing.T, s server) (*config.Config, string) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg := setupDB(t, s, m1DB)
	if _, stderr, code := run(t, cfg, Options{}, m1Schema); code != 0 {
		t.Fatalf("schema: %s", stderr)
	}
	return cfg, filepath.Join(xdg, "dbcc", "commands")
}

// multiConfig writes profiles "it" and "it2" (same server), with extra
// lines appended to profile "it".
func multiConfig(t *testing.T, s server, extra string) *config.Config {
	t.Helper()
	host, port, _ := net.SplitHostPort(s.addr)
	prof := func(name, more string) string {
		return fmt.Sprintf("[profiles.%s]\ndriver = \"mysql\"\nhost = %q\nport = %s\nuser = \"root\"\ndatabase = %q\npassword = \"env:DBCC_IT_PW\"\n%s\n",
			name, host, port, m1DB, more)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(prof("it", extra)+prof("it2", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func csvLines(out string) []string { return strings.Split(strings.TrimSpace(out), "\n") }

func TestM1DataCommands(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg, _ := m1Setup(t, s)
			cases := []struct {
				cmd   string
				lines int    // csv lines incl. header
				want  string // substring of stdout
			}{
				{"ll customers", 13, "id,name,city"},
				{"ll /it/" + m1DB + "/customers", 13, "12,l,z"},
				{"ll " + m1DB + ".customers", 13, "1,a,x"},
				{"ll `customers`", 13, "3,c,\n"},
				{"ll v_city", 4, "city"},
				{"head customers", 11, "10,j,z"},
				{"head customers 3", 4, "3,c,"},
				{"head customers 0", 1, "id,name,city"},
				{"head v_city 2", 3, "city"},
				{"wc customers", 2, "rows,object\n12,customers"},
				{"wc /it/" + m1DB + "/v_city", 2, "3,v_city"},
			}
			for _, c := range cases {
				out, stderr, code := run(t, cfg, Options{Command: c.cmd, Format: "csv"}, "")
				if code != 0 {
					t.Errorf("%s: exit %d: %s", c.cmd, code, stderr)
					continue
				}
				if n := len(csvLines(out)); n != c.lines || !strings.Contains(out, c.want) {
					t.Errorf("%s: %d lines (want %d), want %q in:\n%s", c.cmd, n, c.lines, c.want, out)
				}
			}
			// Table output of ll (default format).
			out, _, code := run(t, cfg, Options{Command: "ll customers"}, "")
			if code != 0 || !strings.Contains(out, "id | name | city") || !strings.Contains(out, "NULL") {
				t.Errorf("ll table format:\n%s", out)
			}
			// Failures: command errors exit 1, usage errors exit 2.
			fails := []struct {
				cmd  string
				code int
				want string
			}{
				{"ll nosuch", ExitSQL, "ll: no such object: nosuch in /it/" + m1DB},
				{"ll p_noop", ExitSQL, "ll: p_noop is a procedure, not a table or view"},
				{"wc /it/nodb/customers", ExitSQL, "no such object"},
				{"ll /nosuchprofile/db/t", ExitSQL, `unknown profile "nosuchprofile"`},
				{"head", ExitUsage, "head: usage: head <table|view> [n]"},
				{"head customers x", ExitUsage, "must be an integer"},
				{"wc a b", ExitUsage, "usage: wc <table|view>"},
				{"ll 'customers", ExitUsage, "unterminated quote"},
			}
			for _, f := range fails {
				_, stderr, code := run(t, cfg, Options{Command: f.cmd}, "")
				if code != f.code || !strings.Contains(stderr, f.want) {
					t.Errorf("%s: exit %d (want %d), stderr %q (want %q)", f.cmd, code, f.code, stderr, f.want)
				}
			}
			// SQL still passes through -c.
			out, stderr, code := run(t, cfg, Options{Command: "SELECT COUNT(*) AS n FROM customers", Format: "csv"}, "")
			if code != 0 || strings.TrimSpace(out) != "n\n12" {
				t.Errorf("SQL via -c: %d %q %s", code, out, stderr)
			}
		})
	}
}

func TestM1Ls(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg, _ := m1Setup(t, s)
			out, stderr, code := run(t, cfg, Options{Command: "ls", Format: "csv"}, "")
			if code != 0 {
				t.Fatal(stderr)
			}
			for _, want := range []string{"name,kind,comment", "customers,table,", "v_city,view,", "p_noop,procedure,"} {
				if !strings.Contains(out, want) {
					t.Errorf("ls lacks %q:\n%s", want, out)
				}
			}
			out, _, _ = run(t, cfg, Options{Command: "ls /", Format: "csv"}, "")
			if !strings.Contains(out, "it,connection,mysql ") || !strings.Contains(out, "(current)") {
				t.Errorf("ls /:\n%s", out)
			}
			out, _, _ = run(t, cfg, Options{Command: "ls /it", Format: "csv"}, "")
			if !strings.Contains(out, m1DB+",database,") || !strings.Contains(out, "information_schema,database,(system)") {
				t.Errorf("ls /it:\n%s", out)
			}
			out, _, _ = run(t, cfg, Options{Command: "ls ..", Format: "csv"}, "")
			if !strings.Contains(out, m1DB+",database,") {
				t.Errorf("ls ..:\n%s", out)
			}
			out, _, code = run(t, cfg, Options{Command: "pwd"}, "")
			if code != 0 || strings.TrimSpace(out) != "/it/"+m1DB {
				t.Errorf("pwd: %q", out)
			}
		})
	}
}

// TestM1CdGoAPI drives one headless command layer through several command
// lines, as the UI does (headless -c runs only one command).
func TestM1CdGoAPI(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			m1Setup(t, s)
			cfg := multiConfig(t, s, "")
			var out, errb bytes.Buffer
			env := &Env{Opts: &Options{Profile: "it", Format: "csv"}, Config: cfg, Stdout: &out, Stderr: &errb}
			h := newHeadless(env)
			defer h.ws.CloseAll()
			ctx := context.Background()
			if _, err := h.ws.Chdir(ctx, "/it/"+m1DB); err != nil {
				t.Fatal(err)
			}
			reg, err := h.registry()
			if err != nil {
				t.Fatal(err)
			}
			defer reg.Runtime().Close()
			pwd := func(want string) {
				t.Helper()
				out.Reset()
				if err := reg.Dispatch(ctx, "pwd"); err != nil {
					t.Fatal(err)
				}
				if got := strings.TrimSpace(out.String()); got != want {
					t.Errorf("pwd = %q, want %q", got, want)
				}
			}
			do := func(line string) {
				t.Helper()
				if err := reg.Dispatch(ctx, line); err != nil {
					t.Fatalf("%s: %v", line, err)
				}
			}
			do("cd ..")
			pwd("/it")
			do("cd " + m1DB)
			pwd("/it/" + m1DB)
			do("cd information_schema") // sibling database (leniency in cd.lua)
			pwd("/it/information_schema")
			if s := h.ws.Current().Database(); s != "information_schema" {
				t.Errorf("session database %q", s)
			}
			do("cd -")
			pwd("/it/" + m1DB)
			do("cd /it/mysql")
			pwd("/it/mysql")
			do("cd") // profile default
			pwd("/it/" + m1DB)
			do("cd /")
			pwd("/")
			do("cd") // at "/": no-op
			pwd("/")
			do("cd /it")
			pwd("/it/" + m1DB) // switching connections lands on its database
			if err := reg.Dispatch(ctx, "cd nosuchdb"); err == nil || !strings.HasPrefix(err.Error(), "cd: ") {
				t.Errorf("cd nosuchdb: %v", err)
			}
			pwd("/it/" + m1DB)

			// Absolute object paths do not change the current path, also on
			// another profile (object tree, spec 7.2).
			do("cd /it/information_schema")
			out.Reset()
			do("wc /it2/" + m1DB + "/customers")
			if !strings.Contains(out.String(), "12,customers") {
				t.Errorf("wc on it2: %q", out.String())
			}
			if _, open := h.ws.Sessions()["it2"]; !open {
				t.Errorf("it2 was not opened")
			}
			out.Reset()
			if err := reg.Run(ctx, "head", "/it2/"+m1DB+"/customers", "2"); err != nil {
				t.Fatal(err)
			}
			if n := len(csvLines(out.String())); n != 3 {
				t.Errorf("head on it2: %q", out.String())
			}
			pwd("/it/information_schema")
			// A relative name resolves against the current path.
			if err := reg.Dispatch(ctx, "ll customers"); err == nil {
				t.Errorf("ll customers in information_schema should fail")
			}
			do("cd /it2")
			pwd("/it2/" + m1DB)
			do("cd /it/" + m1DB)
			out.Reset()
			do("ls /")
			if !strings.Contains(out.String(), "it,connection,") || !strings.Contains(out.String(), "(current)") || !strings.Contains(out.String(), "(open)") {
				t.Errorf("ls / with two open profiles:\n%s", out.String())
			}
		})
	}
}

func TestM1UserScripts(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg, dir := m1Setup(t, s)
			os.MkdirAll(dir, 0o700)
			files := map[string]string{
				// adds a command
				"cities.lua": `dbcc.command{ name = "cities", usage = "cities <table>", args = { "object" },
  help = "Distinct cities of a table",
  run = function(ctx, args)
    local t = ctx:resolve(args[1])
    local res = ctx:query("SELECT city, COUNT(*) AS n FROM " .. t.sql .. " WHERE city IS NOT NULL GROUP BY city ORDER BY city")
    local rows = {}
    for _, r in ipairs(res) do rows[#rows + 1] = { r.city, tonumber(r.n) } end
    ctx:rows({ "city", "n" }, rows)
  end }`,
				// replaces a default
				"wc.lua": `dbcc.command{ name = "wc", args = { "object" }, run = function(ctx, args)
    local t = ctx:resolve(args[1])
    local res = ctx:query("SELECT COUNT(*) FROM " .. t.sql .. " WHERE id > ?", 10)
    ctx:text("user-wc " .. res[1][1] .. " " .. #res.columns)
  end }`,
				// broken override: the default ll stays
				"ll.lua": `dbcc.command{ name = "ll", args = { "object" }, run = function(ctx, args) end`,
			}
			for n, src := range files {
				os.WriteFile(filepath.Join(dir, n), []byte(src), 0o600)
			}
			out, stderr, code := run(t, cfg, Options{Command: "cities customers", Format: "csv"}, "")
			if code != 0 || strings.TrimSpace(out) != "city,n\nx,2\ny,3\nz,6" {
				t.Errorf("cities: %d %q %s", code, out, stderr)
			}
			out, _, code = run(t, cfg, Options{Command: "wc customers"}, "")
			if code != 0 || strings.TrimSpace(out) != "user-wc 2 1" {
				t.Errorf("replaced wc: %d %q", code, out)
			}
			out, stderr, code = run(t, cfg, Options{Command: "ll customers", Format: "csv"}, "")
			if code != 0 || len(csvLines(out)) != 13 {
				t.Errorf("ll after broken override: %d %q %s", code, out, stderr)
			}
			if !strings.Contains(stderr, "user command script skipped") || !strings.Contains(stderr, "ll.lua") {
				t.Errorf("broken override not reported: %q", stderr)
			}
			// --no-user-commands: the default wc, no reports.
			out, stderr, code = run(t, cfg, Options{Command: "wc customers", Format: "csv", NoUserCommands: true}, "")
			if code != 0 || !strings.Contains(out, "12,customers") || stderr != "" {
				t.Errorf("--no-user-commands: %d %q %q", code, out, stderr)
			}
			if _, _, code := run(t, cfg, Options{Command: "cities customers", NoUserCommands: true}, ""); code == 0 {
				t.Errorf("cities exists with --no-user-commands (ran as SQL successfully?)")
			}
		})
	}
}

func TestM1WriteCommand(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			_, dir := m1Setup(t, s)
			os.MkdirAll(dir, 0o700)
			os.WriteFile(filepath.Join(dir, "addc.lua"), []byte(`dbcc.command{ name = "addc", write = true,
  args = { "int", "string" },
  run = function(ctx, args)
    local n = ctx:exec("INSERT INTO customers (id, name) VALUES (?, ?)", tonumber(args[1]), args[2])
    ctx:message("inserted " .. n)
  end }`), 0o600)
			cfg := multiConfig(t, s, "")
			_, stderr, code := run(t, cfg, Options{Command: "addc 100 zz"}, "")
			if code != ExitSQL || !strings.Contains(stderr, "confirmation required") || !strings.Contains(stderr, "INSERT INTO customers") {
				t.Errorf("without --yes: %d %q", code, stderr)
			}
			_, stderr, code = run(t, cfg, Options{Command: "addc 100 zz", Yes: true}, "")
			if code != 0 || !strings.Contains(stderr, "inserted 1") {
				t.Errorf("with --yes: %d %q", code, stderr)
			}
			out, _, _ := run(t, cfg, Options{Command: "wc customers", Format: "csv"}, "")
			if !strings.Contains(out, "13,customers") {
				t.Errorf("row not inserted: %q", out)
			}
			// confirm_writes + write command: one confirmation, no second refusal.
			cw := multiConfig(t, s, "confirm_writes = true")
			if _, stderr, code := run(t, cw, Options{Command: "addc 101 zz", Yes: true}, ""); code != 0 {
				t.Errorf("confirm_writes: %d %q", code, stderr)
			}
			// read-only profile: the write command is blocked.
			ro := multiConfig(t, s, "read_only = true")
			_, stderr, code = run(t, ro, Options{Command: "addc 102 zz", Yes: true}, "")
			if code != ExitSQL || !strings.Contains(stderr, "addc: writes and is blocked in the read-only profile it") {
				t.Errorf("read-only: %d %q", code, stderr)
			}
			// read commands still work there
			if out, stderr, code := run(t, ro, Options{Command: "wc customers", Format: "csv"}, ""); code != 0 || !strings.Contains(out, "14,customers") {
				t.Errorf("read-only wc: %d %q %q", code, out, stderr)
			}
		})
	}
}
