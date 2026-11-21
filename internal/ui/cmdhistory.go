package ui

// Persistent command-line history (owner request 2026-11-22): the lines
// entered in the command line survive a restart, like the SQL history.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	cmdHistoryFile = "cmdline_history"
	cmdHistoryMax  = 1000
)

// secretRe matches lines that must not be written to disk (same rule as
// the statement history, spec 12).
var secretRe = regexp.MustCompile(`(?i)identified\s+by|password`)

func (a *App) cmdHistoryPath() string { return filepath.Join(a.opts.StateDir, cmdHistoryFile) }

// loadCmdHistory reads the last cmdHistoryMax lines (UI goroutine, start).
func (a *App) loadCmdHistory() {
	f, err := os.Open(a.cmdHistoryPath())
	if err != nil {
		return
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > cmdHistoryMax {
		lines = lines[len(lines)-cmdHistoryMax:]
	}
	a.cmdHistory = lines
	a.histPos = len(lines)
}

// rememberCmd adds a line to the history and appends it to the file. The
// file is compacted to cmdHistoryMax lines when it has grown to twice that.
func (a *App) rememberCmd(line string) {
	if n := len(a.cmdHistory); n > 0 && a.cmdHistory[n-1] == line {
		a.histPos = n // no consecutive duplicates
		return
	}
	a.cmdHistory = append(a.cmdHistory, line)
	if len(a.cmdHistory) > cmdHistoryMax {
		a.cmdHistory = a.cmdHistory[len(a.cmdHistory)-cmdHistoryMax:]
	}
	a.histPos = len(a.cmdHistory)
	if secretRe.MatchString(line) || strings.ContainsAny(line, "\n\r") {
		return // kept for this session only
	}
	if err := os.MkdirAll(a.opts.StateDir, 0o700); err != nil {
		return
	}
	path := a.cmdHistoryPath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	f.WriteString(line + "\n")
	st, _ := f.Stat()
	f.Close()
	if st != nil && st.Size() > 2*cmdHistoryMax*80 {
		a.compactCmdHistory()
	}
}

func (a *App) compactCmdHistory() {
	var keep []string
	for _, l := range a.cmdHistory {
		if !secretRe.MatchString(l) {
			keep = append(keep, l)
		}
	}
	tmp := a.cmdHistoryPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(keep, "\n")+"\n"), 0o600); err == nil {
		os.Rename(tmp, a.cmdHistoryPath())
	}
}
