//go:build integration

package script_test

// Golden tests of cat on PostgreSQL and SQL Server with the rich test
// schemas of the dialects (internal/dialect/{postgres,mssql}/testdata):
//
//	DBCC_IT_POSTGRES=127.0.0.1:15432 DBCC_IT_PG_PW=<postgres password>
//	DBCC_IT_MSSQL=127.0.0.1:11433    DBCC_IT_MSSQL_PW=<sa password>
//	go test -tags integration -count=1 ./internal/script/ [-update]

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbcc/internal/command"
	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/script"
	"dbcc/internal/session"

	_ "dbcc/internal/dialect/mssql"
	_ "dbcc/internal/dialect/postgres"
)

const catDXDB = "dbcc_it_cat"

type dxCatServer struct {
	name, env, user, pwEnv, params, adminDB, schema, create, drop string
	objects                                                       []struct{ file, line string }
}

type catObj = struct{ file, line string }

var dxCatServers = []dxCatServer{
	{name: "postgres", env: "DBCC_IT_POSTGRES", user: "postgres", pwEnv: "DBCC_IT_PG_PW", params: `sslmode = "disable"`,
		adminDB: "postgres", schema: "app", create: "CREATE DATABASE " + catDXDB,
		drop: "DROP DATABASE IF EXISTS " + catDXDB + " WITH (FORCE)",
		objects: []catObj{
			{"items", "cat items"}, {"parent", "cat parent"}, {"logs", "cat app.logs"}, {"logs_2024", "cat logs_2024"},
			{"v_items", "cat v_items"}, {"mv_totals", "cat mv_totals"}, {"p_count", "cat -t procedure p_count"},
			{"plain", "cat public.plain"},
		}},
	{name: "mssql", env: "DBCC_IT_MSSQL", user: "sa", pwEnv: "DBCC_IT_MSSQL_PW", params: `TrustServerCertificate = "true"`,
		adminDB: "master", schema: "sales", create: "CREATE DATABASE " + catDXDB + " COLLATE Latin1_General_100_CI_AS",
		drop: "IF DB_ID('" + catDXDB + "') IS NOT NULL BEGIN ALTER DATABASE " + catDXDB +
			" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE " + catDXDB + " END",
		objects: []catObj{
			{"items", "cat items"}, {"parent", "cat dbo.parent"}, {"logs", "cat /it/" + catDXDB + "/dbo/logs"},
			{"v_items", "cat v_items"}, {"calc_total", "cat calc_total"}, {"add_tax", "cat dbo.add_tax"},
			{"trg_items_ins", "cat -t trigger trg_items_ins"},
		}},
}

func setupDX(t *testing.T, s dxCatServer) *fixture {
	t.Helper()
	addr := os.Getenv(s.env)
	host, port, _ := net.SplitHostPort(addr)
	path := filepath.Join(t.TempDir(), "config.toml")
	body := fmt.Sprintf("[profiles.it]\ndriver = %q\nhost = %q\nport = %s\nuser = %q\npassword = \"env:%s\"\ndatabase = %q\nparams = { %s }\n",
		s.name, host, port, s.user, s.pwEnv, s.adminDB, s.params)
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
	sql(s.drop)
	sql(s.create)
	t.Cleanup(func() {
		ws.Chdir(context.Background(), "/it/"+s.adminDB)
		runner(ws.Current()).RunScript(context.Background(), s.drop, 0)
	})
	if _, err := ws.Chdir(ctx, "/it/"+catDXDB); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "dialect", s.name, "testdata", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql(string(b))
	if _, err := ws.Chdir(ctx, "/it/"+catDXDB+"/"+s.schema); err != nil {
		t.Fatal(err)
	}
	rt := script.New(script.Env{Workspace: ws, Host: h, Runner: runner})
	t.Cleanup(rt.Close)
	reg := command.New(rt)
	if err := reg.LoadDefaults(); err != nil {
		t.Fatal(err)
	}
	return &fixture{reg: reg, host: h, ws: ws}
}

func TestCatGoldenDX(t *testing.T) {
	ran := false
	for _, s := range dxCatServers {
		if os.Getenv(s.env) == "" {
			continue
		}
		ran = true
		t.Run(s.name, func(t *testing.T) {
			f := setupDX(t, s)
			for _, o := range s.objects {
				f.host.texts = nil
				if err := f.reg.Dispatch(context.Background(), o.line); err != nil {
					t.Errorf("%s: %v", o.line, err)
					continue
				}
				if len(f.host.texts) != 1 {
					t.Errorf("%s: %d texts", o.line, len(f.host.texts))
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
			// Everything the catalog knows appears (spec 8.1).
			f.host.texts = nil
			if err := f.reg.Dispatch(context.Background(), "cat items"); err != nil {
				t.Fatal(err)
			}
			out := f.host.texts[0]
			want := map[string][]string{
				"postgres": {"GENERATED ALWAYS AS IDENTITY", "uq_code_region", "fk_parent", "chk_qty", "chk_code",
					"ix_items_active", "| Include", "| Filter", "status = 'active'", "trg_items_bi", "trg_items_trunc",
					"items with everything", "surrogate key", "gin"},
				"mssql": {"IDENTITY(", "Include", "Filter", "([qty]>(0))", "name, qty", "trg_items_ins", "CIX_items_created"},
			}[s.name]
			for _, w := range want {
				if !strings.Contains(out, w) {
					t.Errorf("cat items lacks %q:\n%s", w, out)
				}
			}
		})
	}
	if !ran {
		t.Skip("no DBCC_IT_POSTGRES / DBCC_IT_MSSQL servers configured")
	}
}
