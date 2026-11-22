package command

import (
	"dbcc/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Lua syntax error ends with a newline in gopher-lua; the report must be
// a single line (it left an empty line in the message log).
func TestBrokenScriptReportIsOneLine(t *testing.T) {
	r, h := newRegistry(t, &config.Config{Settings: config.DefaultSettings(), Profiles: map[string]*config.Profile{}})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "broken.lua"), []byte("dbcc.command{ name = "), 0o600)
	r.LoadUserDir(dir)
	found := false
	for _, m := range h.messages {
		if m.Err != nil && strings.Contains(m.Err.Error(), "broken.lua") {
			found = true
			if strings.ContainsAny(m.Err.Error(), "\n\r") || m.Info != "" {
				t.Fatalf("report not a single line: %q / info %q", m.Err.Error(), m.Info)
			}
		}
	}
	if !found {
		t.Fatal("broken script not reported")
	}
}
