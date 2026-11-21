package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/script"
	"dbcc/internal/session"
)

type host struct {
	results  []*exec.Result
	texts    []string
	messages []exec.Message
}

func (h *host) Result(r *exec.Result)                              { h.results = append(h.results, r) }
func (h *host) Text(_, text, _ string)                             { h.texts = append(h.texts, text) }
func (h *host) Message(m exec.Message)                             { h.messages = append(h.messages, m) }
func (h *host) Confirm(string) (bool, error)                       { return false, nil }
func (h *host) Pick(string, []string) (int, error)                 { return -1, script.ErrNoUI }
func (h *host) EditorSet(string) error                             { return script.ErrNoUI }
func (h *host) EditorOpen(string, string, *script.ObjectRef) error { return nil }
func (h *host) About() error                                       { return nil }
func (h *host) ActiveResult() (*exec.Result, exec.View)            { return nil, nil }

func newRegistry(t *testing.T, cfg *config.Config) (*Registry, *host) {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{Profiles: map[string]*config.Profile{}}
	}
	h := &host{}
	rt := script.New(script.Env{Workspace: session.NewWorkspace(cfg, nil), Host: h})
	t.Cleanup(rt.Close)
	return New(rt), h
}

func TestSplit(t *testing.T) {
	cases := map[string][]string{
		"ll customers":           {"ll", "customers"},
		"  head   t  5 ":         {"head", "t", "5"},
		`cd 'my db'`:             {"cd", "my db"},
		`cd "my \"db\""`:         {"cd", `my "db"`},
		`cd my\ db`:              {"cd", "my db"},
		`x 'a'"b"c`:              {"x", "abc"},
		`x ''`:                   {"x", ""},
		`x 'it\s'`:               {"x", `it\s`},
		`x "a\nb"`:               {"x", `a\nb`},
		"":                       nil,
		"grep \"%\tfoo\" /a/b.c": {"grep", "%\tfoo", "/a/b.c"},
	}
	for in, want := range cases {
		got, err := Split(in)
		if err != nil || strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
			t.Errorf("Split(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{`x 'a`, `x "a`, `x "a\"`} {
		if _, err := Split(bad); !errors.Is(err, ErrUnterminated) {
			t.Errorf("Split(%q): want ErrUnterminated, got %v", bad, err)
		}
	}
}

func TestDefaultsLoad(t *testing.T) {
	r, _ := newRegistry(t, nil)
	if err := r.LoadDefaults(); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range r.Commands() {
		names = append(names, c.Name)
		if !c.Default || !strings.HasPrefix(c.Source, "embedded:") || c.Help == "" || c.Usage == "" {
			t.Errorf("%s: %+v", c.Name, c)
		}
	}
	// Every command of spec section 8, one file each.
	if got := strings.Join(names, " "); got != "cat cd cp du edit export grep head help history kill ll ls mv ps pwd reload tail wc" {
		t.Fatalf("defaults: %s", got)
	}
	files, _ := DefaultScripts()
	if len(files) != len(names) {
		t.Errorf("%d scripts for %d commands", len(files), len(names))
	}
	for _, c := range r.Commands() {
		if c.Source != "embedded:"+c.Name+".lua" {
			t.Errorf("%s comes from %s", c.Name, c.Source)
		}
	}
	if got := strings.Join(r.Complete("l"), " "); got != "ll ls" {
		t.Errorf("complete: %s", got)
	}
}

func write(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUserOverrideAndFallback(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(config.ConfigDir(), "commands")
	os.MkdirAll(dir, 0o700)
	write(t, dir, "hello.lua", `dbcc.command{ name = "hello", args = {"string?"}, help = "greet",
  run = function(ctx, args) ctx:text("hello " .. (args[1] or "world")) end }`)
	write(t, dir, "pwd.lua", `dbcc.command{ name = "pwd", run = function(ctx) ctx:text("user pwd " .. ctx.path.str) end }`)
	write(t, dir, "cd.lua", `dbcc.command{ name = "cd", run = function(ctx) end`)                        // syntax error
	write(t, dir, "ls.lua", `dbcc.command{ name = "ls", run = function() end } error("broken at load")`) // runtime error
	write(t, dir, "wc.lua", `dbcc.command{ name = "wc", args = {"object"} }`)                            // invalid registration

	r, h := newRegistry(t, nil)
	if err := r.Load(Options{UserDir: dir}); err != nil {
		t.Fatal(err)
	}
	if len(h.messages) != 3 {
		t.Fatalf("want 3 load reports, got %d: %+v", len(h.messages), h.messages)
	}
	for i, f := range []string{"cd.lua", "ls.lua", "wc.lua"} {
		m := h.messages[i]
		if m.Err == nil || !strings.Contains(m.Info, f) || !strings.Contains(m.Info, "skipped") {
			t.Errorf("report %d: %+v", i, m)
		}
	}
	for _, n := range []string{"cd", "ls", "wc"} {
		c, _ := r.Lookup(n)
		if !c.Default {
			t.Errorf("%s: broken override replaced the default (%s)", n, c.Source)
		}
	}
	if c, _ := r.Lookup("pwd"); c.Default || !c.Replaces || !strings.HasSuffix(c.Source, "pwd.lua") {
		t.Errorf("pwd override: %+v", c)
	}
	ctx := context.Background()
	if err := r.Dispatch(ctx, "hello 'dear owner'"); err != nil {
		t.Fatal(err)
	}
	if err := r.Dispatch(ctx, "pwd"); err != nil {
		t.Fatal(err)
	}
	// The default cd still works after its override failed: "cd" at "/" is a no-op.
	if err := r.Dispatch(ctx, "cd"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.texts, "|"); got != "hello dear owner|user pwd /" {
		t.Errorf("texts: %q", got)
	}

	// --no-user-commands: defaults only.
	r2, h2 := newRegistry(t, nil)
	if err := r2.Load(Options{UserDir: dir, NoUserCommands: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r2.Lookup("hello"); ok || len(h2.messages) != 0 {
		t.Errorf("user commands loaded despite NoUserCommands")
	}
}

func TestDispatch(t *testing.T) {
	cfg := &config.Config{Profiles: map[string]*config.Profile{
		"b-prof": {Name: "b-prof", Driver: "mysql", Host: "db.example", Port: 3306, Database: "shop"},
		"a-prof": {Name: "a-prof", Driver: "postgres", Socket: "/run/pg"},
	}}
	r, h := newRegistry(t, cfg)
	if err := r.LoadDefaults(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Not a command: SQL, which needs a connection.
	if err := r.Dispatch(ctx, "SELECT 1"); !errors.Is(err, ErrNoSession) {
		t.Errorf("SQL at /: %v", err)
	}
	// Arity is checked before the command runs.
	var ue *script.UsageError
	if err := r.Dispatch(ctx, "head"); !errors.As(err, &ue) {
		t.Errorf("head without args: %v", err)
	}
	if err := r.Dispatch(ctx, "head t x"); !errors.As(err, &ue) {
		t.Errorf("head t x: %v", err)
	}
	if err := r.Dispatch(ctx, "ll 'unterminated"); !errors.As(err, &ue) {
		t.Errorf("unterminated quote: %v", err)
	}
	// A command that needs a connection fails with its name.
	var ce *script.CommandError
	if err := r.Dispatch(ctx, "ll customers"); !errors.As(err, &ce) || !strings.HasPrefix(err.Error(), "ll: ") {
		t.Errorf("ll at /: %v", err)
	}
	// ls at / lists the profiles.
	if err := r.Dispatch(ctx, "ls"); err != nil {
		t.Fatal(err)
	}
	res := h.results[len(h.results)-1]
	rows := res.Rows()
	if len(rows) != 2 || rows[0][0] != "a-prof" || rows[0][1] != "connection" || rows[0][2] != "postgres /run/pg" ||
		rows[1][2] != "mysql db.example:3306/shop" {
		t.Errorf("ls /: %v", rows)
	}
	if err := r.Dispatch(ctx, "pwd;"); err != nil || h.texts[len(h.texts)-1] != "/" {
		t.Errorf("pwd: %v %v", err, h.texts)
	}
	if err := r.Run(ctx, "nosuch"); !errors.As(err, &ue) {
		t.Errorf("Run unknown: %v", err)
	}
}
