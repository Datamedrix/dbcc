package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dbcc/internal/branding"
	"dbcc/internal/command"
	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/history"
	"dbcc/internal/script"
	"dbcc/internal/script/commands"
	"dbcc/internal/session"
)

// headless bundles the workspace, the per-session runners and the host of
// headless mode.
type headless struct {
	env     *Env
	ws      *session.Workspace
	sink    *headlessSink
	runners map[*session.Session]*exec.Runner
	hist    *history.History
}

func newHeadless(env *Env) *headless {
	h := &headless{
		env:     env,
		ws:      session.NewWorkspace(env.Config, env.Prompt),
		sink:    &headlessSink{env: env},
		runners: map[*session.Session]*exec.Runner{},
	}
	h.hist = OpenHistory(env.Stderr)
	return h
}

// OpenHistory opens the statement history in the state directory (spec
// 12). A failure is reported to warn (if not nil) and yields nil: the tool
// keeps working without history. The TUI calls it once at start, passes
// the result as script.Env.History and installs History.Hook(s) as (or
// chained into) Runner.AfterStatement of every runner.
func OpenHistory(warn io.Writer) *history.History {
	hist, err := history.Open(config.StateDir())
	if err != nil {
		if warn != nil {
			fmt.Fprintf(warn, "warning: %v (statements are not recorded)\n", err)
		}
		return nil
	}
	return hist
}

// runner returns the runner of a session, creating it with the settings.
func (h *headless) runner(s *session.Session) *exec.Runner {
	if r, ok := h.runners[s]; ok {
		return r
	}
	r := exec.NewRunner(s, h.sink)
	r.FetchSize = h.env.Config.Settings.FetchSize
	r.MaxRows = 0 // headless output is streamed, not capped
	r.MaxBuffered = h.env.Config.Settings.MaxRowsBuffered
	yes := h.env.Opts.Yes
	r.Confirm = func(string) bool { return yes }
	r.AfterStatement = h.hist.Hook(s)
	h.runners[s] = r
	return r
}

// registry creates the Lua runtime and loads the commands (embedded
// defaults, then the user scripts unless --no-user-commands).
func (h *headless) registry() (*command.Registry, error) {
	rt := script.New(script.Env{Workspace: h.ws, Host: h.sink, Runner: h.runner, History: h.hist})
	reg := command.New(rt)
	err := reg.Load(command.Options{
		NoUserCommands: h.env.Opts.NoUserCommands,
		UserDir:        filepath.Join(config.ConfigDir(), "commands"),
	})
	if err != nil {
		rt.Close()
		return nil, err
	}
	return reg, nil
}

// ---- script.Host for headless mode (Result and Message are in headless.go) ----

var _ script.Host = (*headlessSink)(nil)

func (h *headlessSink) Text(title, text, lexer string) {
	if h.results > 0 && h.env.Opts.Format == "table" {
		fmt.Fprintln(h.env.Stdout)
	}
	h.results++
	io.WriteString(h.env.Stdout, text)
	if !strings.HasSuffix(text, "\n") {
		io.WriteString(h.env.Stdout, "\n")
	}
}

// ErrNeedsYes is returned by Confirm in headless mode without --yes.
var ErrNeedsYes = errors.New("confirmation required; run with --yes")

func (h *headlessSink) Confirm(msg string) (bool, error) {
	if !h.env.Opts.Yes {
		return false, fmt.Errorf("%w:\n%s", ErrNeedsYes, msg)
	}
	if h.env.Opts.Verbose {
		fmt.Fprintf(h.env.Stderr, "-- confirmed (--yes): %s\n", msg)
	}
	return true, nil
}

func (h *headlessSink) Pick(title string, items []string) (int, error) {
	return -1, fmt.Errorf("choosing from a list is %w", script.ErrNoUI)
}

func (h *headlessSink) EditorSet(text string) error {
	return fmt.Errorf("the editor is %w", script.ErrNoUI)
}

func (h *headlessSink) EditorOpen(title, text string, obj *script.ObjectRef) error {
	h.Text(title, text, "")
	return nil
}

func (h *headlessSink) About() error {
	lines := []string{branding.Title + " " + branding.Version, branding.Subtitle, "", "Datamedrix",
		branding.Company, branding.Contact + " <" + branding.Email + ">"}
	if branding.BuildDate != "" {
		lines[0] += " (" + branding.BuildDate + ")"
	}
	h.Text("About", strings.Join(lines, "\n"), "")
	return nil
}

// ExportCommands writes the embedded command scripts to dir
// (--export-commands). It creates dir if needed and never overwrites an
// existing file: if any target exists, nothing is written.
func ExportCommands(dir string, stderr io.Writer) error {
	names, err := command.DefaultScripts()
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			return &UsageError{fmt.Errorf("%s already exists; refusing to overwrite (choose an empty directory)", filepath.Join(dir, n))}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &UsageError{err}
	}
	for _, n := range names {
		b, err := commands.FS.ReadFile(n)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, n), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return &UsageError{err}
		}
		_, werr := f.Write(b)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return &UsageError{werr}
		}
	}
	fmt.Fprintf(stderr, "wrote %d command scripts to %s\n", len(names), dir)
	return nil
}
