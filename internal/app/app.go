// Package app wires configuration, sessions, the runner, the Lua command
// layer and the UI. It implements both TUI mode and headless mode.
package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"dbcc/internal/config"
	"dbcc/internal/exec"
	"dbcc/internal/script"
)

// Exit codes (spec 15).
const (
	ExitOK    = 0
	ExitSQL   = 1
	ExitUsage = 2
)

// Options are the parsed command-line options.
type Options struct {
	Profile        string
	File           string
	Command        string
	Format         string
	Yes            bool
	NoSplash       bool
	NoUserCommands bool
	ExportCommands string
	ConfigPath     string
	Verbose        bool
	Version        bool
}

// UsageError is a command-line or configuration error (exit code 2).
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

func usagef(format string, a ...any) error { return &UsageError{fmt.Errorf(format, a...)} }

// ParseArgs parses the command line (without the program name).
func ParseArgs(args []string, stderr io.Writer) (*Options, error) {
	o := &Options{}
	fs := flag.NewFlagSet("dbcc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.Profile, "p", "", "connection profile")
	fs.StringVar(&o.File, "f", "", "run a SQL script without UI")
	fs.StringVar(&o.Command, "c", "", "run one command or statement without UI")
	fs.StringVar(&o.Format, "format", "table", "output format for results: table|csv|tsv|json")
	fs.BoolVar(&o.Yes, "yes", false, "answer confirmations with yes")
	fs.BoolVar(&o.NoSplash, "no-splash", false, "skip the splash screen")
	fs.BoolVar(&o.NoUserCommands, "no-user-commands", false, "load only the embedded default commands")
	fs.StringVar(&o.ExportCommands, "export-commands", "", "write the embedded command scripts to a directory")
	fs.StringVar(&o.ConfigPath, "config", "", "configuration file (default "+config.DefaultPath()+")")
	fs.BoolVar(&o.Verbose, "v", false, "headless: log every statement to stderr")
	fs.BoolVar(&o.Version, "version", false, "print the version")
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage:
  dbcc [profile]                          start the UI, optionally connect
  dbcc -p <profile> -f <file.sql>         run a script without UI
  dbcc -p <profile> -c "<command|SQL>"    run one command or statement without UI
  dbcc -p <profile> < file.sql            stdin is not a terminal: run without UI

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, err
		}
		return nil, &UsageError{err}
	}
	switch fs.NArg() {
	case 0:
	case 1:
		if o.Profile != "" && o.Profile != fs.Arg(0) {
			return nil, usagef("profile given twice: -p %s and %s", o.Profile, fs.Arg(0))
		}
		o.Profile = fs.Arg(0)
	default:
		return nil, usagef("too many arguments: %s", strings.Join(fs.Args(), " "))
	}
	if o.File != "" && o.Command != "" {
		return nil, usagef("-f and -c cannot be combined")
	}
	switch o.Format {
	case "table", "csv", "tsv", "json":
	default:
		return nil, usagef("unknown format %q", o.Format)
	}
	return o, nil
}

// Headless reports whether the options select headless mode.
func (o *Options) Headless(stdin *os.File) bool {
	return o.File != "" || o.Command != "" || !term.IsTerminal(int(stdin.Fd()))
}

// ExitCode maps an error to the process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ue *UsageError
	if errors.As(err, &ue) {
		return ExitUsage
	}
	var cu *script.UsageError
	if errors.As(err, &cu) {
		return ExitUsage
	}
	var se *exec.StmtError
	if errors.As(err, &se) {
		return ExitSQL
	}
	var sqlErr *ScriptError
	if errors.As(err, &sqlErr) {
		return ExitSQL
	}
	return ExitUsage
}

// ScriptError is a failure of a command script (exit code 1).
type ScriptError struct{ Err error }

func (e *ScriptError) Error() string { return e.Err.Error() }
func (e *ScriptError) Unwrap() error { return e.Err }

// TTYPrompter reads a password from the controlling terminal without echo.
func TTYPrompter(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("password prompt needs a terminal: %w", err)
	}
	defer tty.Close()
	fmt.Fprint(tty, prompt)
	pw, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	return string(pw), err
}
