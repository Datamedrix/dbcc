//go:build integration

package session

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"dbcc/internal/config"
	"dbcc/internal/sshtunnel/sshtest"

	_ "dbcc/internal/dialect/mssql"
	_ "dbcc/internal/dialect/mysql"
	_ "dbcc/internal/dialect/postgres"
)

// TestSSHTunnelToServers connects every test server through an SSH tunnel
// (A27): host key question, SSH password prompted once and stored, then
// reused; statements and (PostgreSQL) cancel requests go through the
// tunnel.
func TestSSHTunnelToServers(t *testing.T) {
	type srv struct {
		driver, env, user, pwEnv string
		params                   map[string]string
	}
	servers := map[string]srv{
		"mysql":    {"mysql", "DBCC_IT_MYSQL", "root", "DBCC_IT_PW", nil},
		"mariadb":  {"mysql", "DBCC_IT_MARIADB", "root", "DBCC_IT_PW", nil},
		"postgres": {"postgres", "DBCC_IT_POSTGRES", "postgres", "DBCC_IT_PG_PW", map[string]string{"sslmode": "disable"}},
		"mssql": {"mssql", "DBCC_IT_MSSQL", "sa", "DBCC_IT_MSSQL_PW", map[string]string{"TrustServerCertificate": "true",
			// makes the driver set a deadline on every read and write
			// (unsupported by plain SSH channels, see deadlineConn)
			"connection timeout": "30"}},
	}
	ran := 0
	for name, sv := range servers {
		addr := os.Getenv(sv.env)
		if addr == "" || os.Getenv(sv.pwEnv) == "" {
			continue
		}
		ran++
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("SSH_AUTH_SOCK", "")
			old := SSHConfigPath
			SSHConfigPath = func() string { return filepath.Join(home, ".ssh", "config") }
			defer func() { SSHConfigPath = old }()

			ssh := sshtest.Start(t, sshtest.Options{Password: "tunnel-pw"})
			sh, sp, _ := net.SplitHostPort(ssh.Addr)
			sport, _ := strconv.Atoi(sp)
			dh, dp, _ := net.SplitHostPort(addr)
			dport, _ := strconv.Atoi(dp)
			p := &config.Profile{Name: "tun", Driver: sv.driver, Host: dh, Port: dport, User: sv.user,
				Password: "env:" + sv.pwEnv, Params: sv.params,
				SSH: &config.SSH{Host: sh, Port: sport, User: "jumper", Password: "stored"}}
			cfg := &config.Config{Profiles: map[string]*config.Profile{"tun": p}}

			prompts, questions := 0, 0
			ws := NewWorkspace(cfg, func(prompt string) (string, error) {
				prompts++
				if !strings.Contains(prompt, "jumper@") {
					t.Errorf("prompt %q", prompt)
				}
				return "tunnel-pw", nil
			})
			ws.ConfirmHostKey = func(msg string) (bool, error) { questions++; return true, nil }
			s, err := ws.Open(context.Background(), "tun")
			if err != nil {
				t.Fatal(err)
			}
			var one int
			if err := s.Main().QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
				t.Fatalf("SELECT 1: %v %d", err, one)
			}
			if prompts != 1 || questions != 1 {
				t.Errorf("prompts %d, host key questions %d", prompts, questions)
			}
			if fw := ssh.Forwards(); len(fw) == 0 || fw[0] != addr {
				t.Errorf("forwards: %v (want %s)", fw, addr)
			}
			if sv.driver == "postgres" {
				// The cancel request goes through the tunnel too: the
				// statement ends at once, the session stays usable.
				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
				start := time.Now()
				_, err := s.Main().ExecContext(ctx, "SELECT pg_sleep(20)")
				cancel()
				if err == nil || time.Since(start) > 5*time.Second {
					t.Errorf("cancel through tunnel: %v after %s", err, time.Since(start))
				}
				if err := s.Main().QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil {
					t.Errorf("after cancel: %v", err)
				}
				if n := len(ssh.Forwards()); n < 2 {
					t.Errorf("the cancel request did not go through the tunnel (%d forwards)", n)
				}
			}
			ws.CloseAll()

			// The SSH password was stored; the host key is known now.
			if kind, sec, ok, _ := config.StoredSSHSecret("tun"); !ok || sec != "tunnel-pw" || !strings.HasPrefix(kind, "password@") {
				t.Fatalf("stored SSH secret: %q %v", sec, ok)
			}
			ws2 := NewWorkspace(cfg, func(string) (string, error) { return "", errors.New("must not ask") })
			s2, err := ws2.Open(context.Background(), "tun")
			if err != nil {
				t.Fatalf("second open with the stored secret: %v", err)
			}
			if err := s2.Main().QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil {
				t.Fatal(err)
			}
			ws2.CloseAll()

			// A wrong DB address shows that the error came through SSH.
			bad := *p
			bad.Port = 1
			cfg.Profiles["bad"] = &bad
			bad.Name = "bad"
			ws3 := NewWorkspace(cfg, func(string) (string, error) { return "tunnel-pw", nil })
			if _, err := ws3.Open(context.Background(), "bad"); err == nil || !strings.Contains(err.Error(), "SSH") {
				t.Errorf("bad port: %v", err)
			}
		})
	}
	if ran == 0 {
		t.Skip("no DBCC_IT_* servers")
	}
}
