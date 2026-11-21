package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/export"
	"dbcc/internal/script"
	"dbcc/internal/session"
)

// Env bundles what headless mode needs; tests construct it directly.
type Env struct {
	Opts   *Options
	Config *config.Config
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Prompt config.Prompter
}

// headlessSink prints results to stdout and messages to stderr.
type headlessSink struct {
	env     *Env
	results int
	err     error
	last    *exec.Result
}

// ActiveResult returns the last result. Headless results are streamed to
// stdout as they arrive, so only rows not yet written remain to export.
func (h *headlessSink) ActiveResult() (*exec.Result, exec.View) { return h.last, nil }

func (h *headlessSink) Result(r *exec.Result) {
	if h.results > 0 && h.env.Opts.Format == "table" {
		fmt.Fprintln(h.env.Stdout)
	}
	h.results++
	h.last = r
	if err := WriteResult(h.env.Stdout, h.env.Opts.Format, r, 500); err != nil && h.err == nil {
		h.err = err
	}
}

func (h *headlessSink) Message(m exec.Message) {
	if m.Stmt == "" && (m.Info != "" || m.Err != nil) {
		// Notes from commands (ctx:message, print) and the command loader.
		text := m.Info
		if text == "" {
			text = m.Err.Error()
		}
		prefix := ""
		if m.Err != nil {
			prefix = "warning: "
		}
		fmt.Fprintf(h.env.Stderr, "%s%s\n", prefix, text)
		return
	}
	for _, w := range m.Warnings {
		fmt.Fprintf(h.env.Stderr, "warning (line %d): %s\n", m.Line, w)
	}
	if m.ImplicitCommit {
		fmt.Fprintf(h.env.Stderr, "note (line %d): statement committed the open transaction implicitly\n", m.Line)
	}
	if !h.env.Opts.Verbose || m.Err != nil {
		return
	}
	var what string
	switch {
	case m.Returned >= 0:
		what = fmt.Sprintf("%d rows", m.Returned)
	case m.Affected >= 0:
		what = fmt.Sprintf("%d rows affected", m.Affected)
	default:
		what = "ok"
	}
	fmt.Fprintf(h.env.Stderr, "-- line %d: %s, %s (%s)\n", m.Line, m.Short(60), what, m.Duration.Round(1000))
}

// WriteResult streams a result (continuing its cursor) in a format.
func WriteResult(w io.Writer, format string, r *exec.Result, chunk int) error {
	names := make([]string, len(r.Columns))
	numeric := make([]bool, len(r.Columns))
	for i, c := range r.Columns {
		names[i], numeric[i] = c.Name, c.Numeric
	}
	wr, err := export.New(format, w, numeric, exec.CellText)
	if err != nil {
		return err
	}
	if err := wr.Header(names); err != nil {
		return err
	}
	written := 0
	for {
		rows := r.Rows()
		for _, row := range rows[written:] {
			if err := wr.Row(row); err != nil {
				return err
			}
		}
		written = len(rows)
		if !r.More() {
			break
		}
		if _, err := r.Fetch(chunk); err != nil {
			wr.Close()
			return err
		}
	}
	return wr.Close()
}

// RunHeadless runs a script file, stdin or a single -c input. -c input
// whose first word is a registered command runs that Lua command.
func RunHeadless(ctx context.Context, env *Env) error {
	o := env.Opts
	if o.Profile == "" {
		return usagef("headless mode needs a profile (-p <profile>)")
	}
	prof, err := env.Config.Profile(o.Profile)
	if err != nil {
		return &UsageError{err}
	}
	h := newHeadless(env)
	defer h.ws.CloseAll()
	start := "/" + prof.Name
	if prof.Database != "" {
		start += "/" + prof.Database
	}
	if _, err := h.ws.Chdir(ctx, start); err != nil {
		return &UsageError{err}
	}

	// Ctrl+C cancels the running statement through the dialect.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		for range sig {
			if s := h.ws.Current(); s != nil {
				s.Cancel(context.Background())
			}
		}
	}()

	var src string
	switch {
	case o.Command != "":
		reg, err := h.registry()
		if err != nil {
			return finish(env, err, h.sink)
		}
		defer reg.Runtime().Close()
		err = reg.Dispatch(ctx, o.Command)
		if err != nil && reg.IsCommand(o.Command) {
			var ue *script.UsageError
			if !errors.As(err, &ue) {
				err = &ScriptError{err}
			}
		}
		return finish(env, err, h.sink)
	case o.File != "":
		b, err := os.ReadFile(o.File)
		if err != nil {
			return &UsageError{err}
		}
		src = string(b)
	default:
		b, err := io.ReadAll(env.Stdin)
		if err != nil {
			return &UsageError{err}
		}
		src = string(b)
	}
	err = h.runner(h.ws.Current()).RunScript(ctx, src, 0)
	return finish(env, err, h.sink)
}

func finish(env *Env, err error, sink *headlessSink) error {
	if err == nil {
		err = sink.err
	}
	if err == nil {
		return nil
	}
	var se *exec.StmtError
	if errors.As(err, &se) {
		fmt.Fprintf(env.Stderr, "ERROR at line %d: %v\n", se.Line, se.Err)
		fmt.Fprintf(env.Stderr, "statement:\n%s\n", indent(se.Stmt))
		return err
	}
	fmt.Fprintf(env.Stderr, "ERROR: %v\n", err)
	return err
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n")
}

func openSession(p *config.Profile, cred config.Credentials) (*session.Session, error) {
	return session.Open(context.Background(), p, cred, "")
}
