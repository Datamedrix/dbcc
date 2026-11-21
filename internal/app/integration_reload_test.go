//go:build integration

package app

import (
	"strings"
	"testing"
)

// TestReloadHeadlessAllServers: reload exists as a default command; in
// headless mode (one command per start) it explains that it is UI-only.
func TestReloadHeadlessAllServers(t *testing.T) {
	for _, srv := range allServers(t) {
		t.Run(srv.name, func(t *testing.T) {
			srv.prepare(t)
			cfg := srv.cfg(t, srv.db, "")
			_, stderr, code := run(t, cfg, Options{Command: "reload"}, "")
			if code != ExitSQL || !strings.Contains(stderr, "only available in the UI") {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
		})
	}
}
