// Package history keeps the statement history (spec 12): every executed
// statement is appended to a JSON-lines file in the state directory with
// timestamp, profile, path, duration and outcome. Statements that contain
// IDENTIFIED BY or PASSWORD are never recorded.
package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"dbcc/internal/exec"
	"dbcc/internal/session"
)

// FileName is the history file inside the state directory.
const FileName = "history.jsonl"

// Entry is one recorded statement.
type Entry struct {
	Time     time.Time `json:"time"`
	Profile  string    `json:"profile"`
	Path     string    `json:"path"`
	Duration float64   `json:"duration_ms"`
	// Outcome is "ok" or the error text.
	Outcome string `json:"outcome"`
	Stmt    string `json:"stmt"`
}

// OK reports whether the statement succeeded.
func (e Entry) OK() bool { return e.Outcome == "ok" }

// History appends to and reads the history file. It is safe for
// concurrent use; a nil *History records nothing and returns no entries.
type History struct {
	mu   sync.Mutex
	path string
}

// Open prepares the history file dir/history.jsonl. The directory is
// created with mode 0700 and the file with mode 0600; an existing file
// with wider permissions is narrowed to 0600.
func Open(dir string) (*History, error) {
	if dir == "" {
		return nil, errors.New("history: no state directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	p := filepath.Join(dir, FileName)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Mode().Perm()&^0o600 != 0 {
		if err := f.Chmod(0o600); err != nil {
			return nil, fmt.Errorf("history: %w", err)
		}
	}
	return &History{path: p}, nil
}

// Path is the history file.
func (h *History) Path() string {
	if h == nil {
		return ""
	}
	return h.path
}

var sensitive = regexp.MustCompile(`(?i)identified\s+by|password`)

// Sensitive reports whether a statement must not be recorded: it contains
// IDENTIFIED BY or PASSWORD (case-insensitive, any whitespace).
func Sensitive(stmt string) bool { return sensitive.MatchString(stmt) }

// Record appends the statement of a runner message. Messages without a
// statement (notes, warnings) and sensitive statements are skipped.
func (h *History) Record(m exec.Message, profile, path string) error {
	if h == nil || strings.TrimSpace(m.Stmt) == "" || Sensitive(m.Stmt) {
		return nil
	}
	e := Entry{Time: m.Time, Profile: profile, Path: path,
		Duration: float64(m.Duration.Microseconds()) / 1000, Outcome: "ok", Stmt: m.Stmt}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if m.Err != nil {
		e.Outcome = m.Err.Error()
		if e.Outcome == "ok" || e.Outcome == "" {
			e.Outcome = "error: " + e.Outcome
		}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	f, err := os.OpenFile(h.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// Hook returns a Runner.AfterStatement function that records every
// statement of session s (profile name and current path at the time of
// the statement). The TUI and headless mode install it on each runner.
func (h *History) Hook(s *session.Session) func(exec.Message) {
	return func(m exec.Message) {
		if h == nil {
			return
		}
		h.Record(m, s.Profile.Name, s.Path().String())
	}
}

// Chain combines AfterStatement hooks; nil hooks are skipped.
func Chain(hooks ...func(exec.Message)) func(exec.Message) {
	return func(m exec.Message) {
		for _, f := range hooks {
			if f != nil {
				f(m)
			}
		}
	}
}

// Last returns the last n entries, oldest first (n <= 0: all). Lines that
// are not valid entries are skipped.
func (h *History) Last(n int) []Entry {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	f, err := os.Open(h.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return readLast(f, n)
}

func readLast(r io.Reader, n int) []Entry {
	var ring []Entry
	start := 0
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var e Entry
			if json.Unmarshal(line, &e) == nil && e.Stmt != "" {
				if n > 0 && len(ring) == n {
					ring[start] = e
					start = (start + 1) % n
				} else {
					ring = append(ring, e)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return append(ring[start:], ring[:start]...)
}
