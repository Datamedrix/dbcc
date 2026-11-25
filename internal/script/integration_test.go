//go:build integration

package script_test

// Golden tests of the cat command against real MySQL and MariaDB servers
// (spec 8.1, 16). Servers come from the environment:
//
//	DBCC_IT_MYSQL=127.0.0.1:13306 DBCC_IT_MARIADB=127.0.0.1:13307 DBCC_IT_PW=<root password>
//	go test -tags integration -count=1 ./internal/script/ [-update]
//
// The schema is the rich test schema of the MySQL dialect
// (internal/dialect/mysql/testdata/schema.sql). -update rewrites
// testdata/golden/cat/<server>/<object>.txt.

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"dbcc/internal/command"
	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/script"
	"dbcc/internal/session"

	_ "dbcc/internal/dialect/mysql"
)

var update = flag.Bool("update", false, "rewrite golden files")

const catDB = "dbcc_it_cat"

type itServer struct{ name, addr string }

func itServers(t *testing.T) []itServer {
	var out []itServer
	for _, s := range []itServer{{"mysql", os.Getenv("DBCC_IT_MYSQL")}, {"mariadb", os.Getenv("DBCC_IT_MARIADB")}} {
		if s.addr != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Skip("no DBCC_IT_* servers configured")
	}
	return out
}

// catHost records texts and results.
type catHost struct {
	texts  []string
	lexers []string
	msgs   []string
}

func (h *catHost) Result(r *exec.Result) {}
func (h *catHost) Text(title, text, lexer string) {
	h.texts = append(h.texts, text)
	h.lexers = append(h.lexers, lexer)
}
func (h *catHost) Message(m exec.Message) {
	if m.Stmt == "" {
		h.msgs = append(h.msgs, m.Info)
	}
}
func (h *catHost) Confirm(string) (bool, error)       { return true, nil }
func (h *catHost) Pick(string, []string) (int, error) { return -1, script.ErrNoUI }
func (h *catHost) Form(string, []script.OptionSpec, map[string]any) (map[string]any, bool, error) {
	return nil, false, script.ErrNoUI
}
func (h *catHost) OpenFile(script.SaveFileOptions) (string, bool, error) {
	return "", false, script.ErrNoUI
}
func (h *catHost) SaveFile(script.SaveFileOptions) (string, bool, error) {
	return "", false, script.ErrNoUI
}
func (h *catHost) EditorSet(string) error                             { return script.ErrNoUI }
func (h *catHost) EditorOpen(string, string, *script.ObjectRef) error { return nil }
func (h *catHost) About() error                                       { return nil }
func (h *catHost) ActiveResult() (*exec.Result, exec.View)            { return nil, nil }

type fixture struct {
	reg  *command.Registry
	host *catHost
	ws   *session.Workspace
}

func setup(t *testing.T, s itServer) *fixture {
	t.Helper()
	host, port, _ := net.SplitHostPort(s.addr)
	path := filepath.Join(t.TempDir(), "config.toml")
	body := fmt.Sprintf("[profiles.it]\ndriver = \"mysql\"\nhost = %q\nport = %s\nuser = \"root\"\npassword = \"env:DBCC_IT_PW\"\n", host, port)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ws := session.NewWorkspace(cfg, nil)
	t.Cleanup(ws.CloseAll)
	if _, err := ws.Chdir(ctx, "/it"); err != nil {
		t.Fatal(err)
	}
	h := &catHost{}
	runners := map[*session.Session]*exec.Runner{}
	runner := func(s *session.Session) *exec.Runner {
		if r, ok := runners[s]; ok {
			return r
		}
		r := exec.NewRunner(s, h)
		runners[s] = r
		return r
	}
	sql := func(src string) {
		t.Helper()
		if err := runner(ws.Current()).RunScript(ctx, src, 0); err != nil {
			t.Fatalf("%v", err)
		}
	}
	sql(fmt.Sprintf("DROP DATABASE IF EXISTS %s; CREATE DATABASE %s", catDB, catDB))
	t.Cleanup(func() { runner(ws.Current()).RunScript(context.Background(), "DROP DATABASE IF EXISTS "+catDB, 0) })
	if _, err := ws.Chdir(ctx, "/it/"+catDB); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"schema.sql", "schema_" + s.name + ".sql"} {
		b, err := os.ReadFile(filepath.Join("..", "dialect", "mysql", "testdata", f))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		sql(string(b))
	}
	rt := script.New(script.Env{Workspace: ws, Host: h, Runner: runner})
	t.Cleanup(rt.Close)
	reg := command.New(rt)
	if err := reg.LoadDefaults(); err != nil {
		t.Fatal(err)
	}
	return &fixture{reg: reg, host: h, ws: ws}
}

var masks = []struct {
	re   *regexp.Regexp
	with string
}{
	// Statistics and counters change with every run.
	{regexp.MustCompile(`(?m)^(rows \(estimate\)|auto_increment)( +\| ).*$`), "$1$2<masked>"},
	{regexp.MustCompile("DEFINER=`[^`]*`@`[^`]*`"), "DEFINER=<definer>"},
	{regexp.MustCompile(`STARTS '[^']*'`), "STARTS <time>"},
}

func mask(s string) string {
	for _, m := range masks {
		s = m.re.ReplaceAllString(s, m.with)
	}
	return s
}

func TestCatGolden(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			f := setup(t, s)
			objects := []struct{ file, line string }{
				{"items", "cat items"},
				{"parent", "cat parent"},
				{"logs", "cat " + catDB + ".logs"},
				{"v_items", "cat v_items"},
				{"p_count", "cat -t procedure p_count"},
				{"f_double", "cat f_double"},
				{"trg_items_bi", "cat -t trigger trg_items_bi"},
				{"ev_cleanup", "cat ev_cleanup"},
			}
			for _, o := range objects {
				f.host.texts, f.host.lexers = nil, nil
				if err := f.reg.Dispatch(context.Background(), o.line); err != nil {
					t.Errorf("%s: %v", o.line, err)
					continue
				}
				if len(f.host.texts) != 1 || f.host.lexers[0] != "mysql" {
					t.Errorf("%s: texts %d lexers %v", o.line, len(f.host.texts), f.host.lexers)
					continue
				}
				got := mask(f.host.texts[0])
				path := filepath.Join("testdata", "golden", "cat", s.name, o.file+".txt")
				if *update {
					os.MkdirAll(filepath.Dir(path), 0o755)
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run with -update)", err)
				}
				if got != string(want) {
					t.Errorf("%s differs from %s:\n--- got\n%s\n--- want\n%s", o.line, path, got, want)
				}
			}
		})
	}
}

// TestCatCompleteness checks that cat items shows every constraint, index,
// trigger and option the catalog reports (spec 8.1, M4 acceptance).
func TestCatCompleteness(t *testing.T) {
	for _, s := range itServers(t) {
		t.Run(s.name, func(t *testing.T) {
			f := setup(t, s)
			if err := f.reg.Dispatch(context.Background(), "cat items"); err != nil {
				t.Fatal(err)
			}
			out := f.host.texts[0]
			sections := []string{"Columns", "Primary and unique keys", "Foreign keys", "Check constraints",
				"Indexes", "Triggers", "Table options"}
			last := -1
			for _, sec := range sections {
				i := strings.Index(out, "\n"+sec+"\n")
				if i < 0 || i < last {
					t.Errorf("section %q missing or out of order", sec)
				}
				last = i
			}
			want := []string{
				"| Field", "| Type", "| Null", "| Key", "| Default", "| Extra", "| Comment",
				"surrogate key", "auto_increment", "'EU'", "'it''s'", "'NULL'", "''", "0.00", "-1.5",
				"GENERATED ALWAYS AS", "STORED", "VIRTUAL", "on update current_timestamp(3)",
				"COLLATE utf8mb4_bin", "enum('new','active','it''s done')", "set('a','b','c')",
				"PRIMARY KEY | PRIMARY", "uq_code_region | code, region",
				"fk_items_parent", "(a, b)", "RESTRICT", "CASCADE",
				"chk_price", "chk_code", "100000",
				"ix_name_prefix", "name(10)", "price DESC, qty", "ix_total", "ft_name", "FULLTEXT", "ix_parent",
				"trg_items_bi", "BEFORE", "INSERT", "trg_items_au", "AFTER", "UPDATE",
				"engine", "InnoDB", "charset", "utf8mb4", "collation", "utf8mb4_unicode_ci", "rows (estimate)",
				"items with everything",
			}
			if s.name == "mysql" {
				want = append(want, "ix_func", "lower(`code`)")
			}
			low := strings.ToLower(out)
			for _, w := range want {
				if !strings.Contains(out, w) && !strings.Contains(low, strings.ToLower(w)) {
					t.Errorf("cat items lacks %q", w)
				}
			}
			f.host.texts = nil
			if err := f.reg.Dispatch(context.Background(), "cat logs"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(f.host.texts[0], "partitioning") || !strings.Contains(f.host.texts[0], "p2023, p2024, pmax") {
				t.Errorf("cat logs lacks partitioning:\n%s", f.host.texts[0])
			}
		})
	}
}
