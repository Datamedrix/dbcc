//go:build integration

package app

// Integration tests against real servers in containers (spec 16, Testing).
// Run with `make itest`; servers are given by environment variables:
//   DBCC_IT_MYSQL=127.0.0.1:13306  DBCC_IT_MARIADB=127.0.0.1:13307
//   DBCC_IT_PW=<root password>

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
	"dbcc/internal/exec"

	_ "dbcc/internal/dialect/mysql"
)

type server struct{ name, addr string }

func servers(t *testing.T) []server {
	var out []server
	for _, s := range []server{{"mysql", os.Getenv("DBCC_IT_MYSQL")}, {"mariadb", os.Getenv("DBCC_IT_MARIADB")}} {
		if s.addr != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Skip("no DBCC_IT_* servers configured")
	}
	return out
}

// itConfig writes a config with profile "it" for the server and database.
func itConfig(t *testing.T, s server, db string) *config.Config {
	t.Helper()
	host, port, _ := net.SplitHostPort(s.addr)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf(`[profiles.it]
driver = "mysql"
host = %q
port = %s
user = "root"
database = %q
password = "env:DBCC_IT_PW"
`, host, port, db)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// run executes headless mode and returns stdout, stderr and the exit code.
func run(t *testing.T, cfg *config.Config, o Options, stdin string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	if o.Format == "" {
		o.Format = "table"
	}
	o.Profile = "it"
	env := &Env{Opts: &o, Config: cfg, Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb}
	err := RunHeadless(context.Background(), env)
	return out.String(), errb.String(), ExitCode(err)
}

// setupDB creates a fresh database for a test.
func setupDB(t *testing.T, s server, db string) *config.Config {
	t.Helper()
	cfg := itConfig(t, s, "")
	_, stderr, code := run(t, cfg, Options{Command: fmt.Sprintf("DROP DATABASE IF EXISTS %s; CREATE DATABASE %s", db, db)}, "")
	if code != 0 {
		t.Fatalf("setup: %s", stderr)
	}
	t.Cleanup(func() { run(t, cfg, Options{Command: "DROP DATABASE IF EXISTS " + db}, "") })
	return itConfig(t, s, db)
}

const procScript = `-- M0 acceptance script
CREATE TABLE t (id INT PRIMARY KEY AUTO_INCREMENT, name VARCHAR(20));
INSERT INTO t (name) VALUES ('a;b'), ("c"), ('d''e');

DELIMITER $$
CREATE PROCEDURE add_row(IN n VARCHAR(20))
BEGIN
  INSERT INTO t (name) VALUES (n);
  SELECT COUNT(*) AS cnt FROM t;
END$$

CREATE FUNCTION twice(x INT) RETURNS INT DETERMINISTIC
BEGIN
  DECLARE y INT;
  SET y = x * 2;
  RETURN y;
END $$
DELIMITER ;

CALL add_row('f');
SELECT name, twice(id) AS t2 FROM t ORDER BY id;
`

func TestM0ScriptWithDelimiter(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := setupDB(t, s, "dbcc_it_m0")
			out, stderr, code := run(t, cfg, Options{}, procScript)
			if code != 0 {
				t.Fatalf("exit %d, stderr:\n%s", code, stderr)
			}
			for _, want := range []string{"cnt", "4", "a;b", "d'e", "f", "8"} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestM0StopsAtFirstError(t *testing.T) {
	script := "CREATE TABLE u (id INT);\nINSERT INTO u VALUES (1);\n\nSELECT nosuchcol\n  FROM u;\nINSERT INTO u VALUES (2);\n"
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := setupDB(t, s, "dbcc_it_err")
			f := filepath.Join(t.TempDir(), "s.sql")
			os.WriteFile(f, []byte(script), 0o600)
			_, stderr, code := run(t, cfg, Options{File: f}, "")
			if code != ExitSQL {
				t.Fatalf("exit code %d, want 1; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, "ERROR at line 4") || !strings.Contains(stderr, "nosuchcol") {
				t.Errorf("stderr lacks line 4 / statement:\n%s", stderr)
			}
			out, _, _ := run(t, cfg, Options{Command: "SELECT COUNT(*) FROM u", Format: "csv"}, "")
			if strings.TrimSpace(out) != "COUNT(*)\n1" {
				t.Errorf("run did not stop at the error; count output %q", out)
			}
		})
	}
}

func TestM0Formats(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := itConfig(t, s, "")
			out, stderr, code := run(t, cfg, Options{Command: "SELECT 1 AS a, NULL AS b, 'x\"y' AS c", Format: "json"}, "")
			if code != 0 {
				t.Fatal(stderr)
			}
			if !strings.Contains(out, `"a": "1", "b": null, "c": "x\"y"`) {
				t.Errorf("json: %s", out)
			}
		})
	}
}

func TestM0ConnectionErrorExit2(t *testing.T) {
	s := server{"none", "127.0.0.1:1"}
	cfg := itConfig(t, s, "")
	_, _, code := run(t, cfg, Options{Command: "SELECT 1"}, "")
	if code != ExitUsage {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestM0LazyCursorPartial(t *testing.T) {
	for _, s := range servers(t) {
		t.Run(s.name, func(t *testing.T) {
			cfg := itConfig(t, s, "")
			prof, _ := cfg.Profile("it")
			cred, err := config.ResolveCredentials(prof, nil)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := openSession(prof, cred)
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()
			r := exec.NewRunner(sess, nil)
			r.FetchSize = 10
			res, err := r.Grid(context.Background(), "SELECT * FROM information_schema.COLUMNS", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !res.More() || res.Len() != 10 {
				t.Fatalf("lazy: more=%v len=%d", res.More(), res.Len())
			}
			// A new statement closes the cursor; the tab keeps its rows, marked partial.
			if _, err := r.Query(context.Background(), "SELECT 1"); err != nil {
				t.Fatal(err)
			}
			if res.More() || !res.Partial || res.Len() != 10 {
				t.Fatalf("after new stmt: more=%v partial=%v len=%d", res.More(), res.Partial, res.Len())
			}
		})
	}
}
