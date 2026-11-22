//go:build integration

package ui

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"golang.org/x/crypto/ssh"

	"dbcc/internal/config"
	"dbcc/internal/session"
	"dbcc/internal/sshtunnel/sshtest"
)

// TestF2SSHTunnelFromSSHConfig: in the connection editor a Host of
// ~/.ssh/config is chosen; Save tests the connection first (host key
// question, SSH password), and only then takes the SSH data over into
// the profile; the next connection needs no question (A27).
func TestF2SSHTunnelFromSSHConfig(t *testing.T) {
	addr := os.Getenv("DBCC_IT_MYSQL")
	if addr == "" {
		t.Skip("no DBCC_IT_MYSQL")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("SSH_AUTH_SOCK", "")
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	srv := sshtest.Start(t, sshtest.Options{Password: "tunnel-pw"})
	sh, sp, _ := net.SplitHostPort(srv.Addr)
	sshCfg := filepath.Join(home, ".ssh", "config")
	os.WriteFile(sshCfg, []byte(fmt.Sprintf("Host tunnelhost\n  HostName %s\n  Port %s\n  User jumper\n", sh, sp)), 0o600)
	old := session.SSHConfigPath
	session.SSHConfigPath = func() string { return sshCfg }
	defer func() { session.SSHConfigPath = old }()

	cfg := emptyConfig()
	cfg.Path = filepath.Join(home, ".config", "dbcc", "config.toml")
	os.MkdirAll(filepath.Dir(cfg.Path), 0o700)
	a, scr, stop := startApp(t, cfg, Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "1Help", 5*time.Second)
	keys(scr, tcell.KeyF2)
	waitFor(t, a, scr, "New connection", 3*time.Second)
	keys(scr, "n")
	waitFor(t, a, scr, "SSH tunnel", 3*time.Second)

	dh, dp, _ := net.SplitHostPort(addr)
	var form *tview.Form
	a.tv.QueueUpdateDraw(func() {
		form = a.profileForm
		set := func(label, v string) { form.GetFormItemByLabel(label).(*tview.InputField).SetText(v) }
		set("Name", "tun")
		set("Host", dh)
		set("Port", dp)
		set("User", "root")
		set("Password", os.Getenv("DBCC_IT_PW"))
		form.GetFormItemByLabel("SSH tunnel").(*tview.DropDown).SetCurrentOption(2) // tunnelhost
		form.GetFormItemByLabel("SSH password").(*tview.DropDown).SetCurrentOption(1)
	})
	// The Host's data was taken from ~/.ssh/config.
	lines := waitFor(t, a, scr, "taken from ~/.ssh/config", 3*time.Second)
	if all := strings.Join(lines, "\n"); !strings.Contains(all, "jumper") || !strings.Contains(all, sp) {
		t.Fatalf("SSH fields not filled:\n%s", all)
	}
	// Save: tests first.
	a.tv.QueueUpdateDraw(func() {
		form.GetButton(1).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
	})
	lines = waitFor(t, a, scr, "can't be established", 5*time.Second)
	if all := strings.Join(lines, "\n"); !strings.Contains(all, ssh.FingerprintSHA256(srv.HostKey.PublicKey())) {
		t.Errorf("fingerprint not shown in one piece:\n%s", all)
	}
	keys(scr, tcell.KeyLeft, tcell.KeyEnter) // "Trust and connect"
	waitFor(t, a, scr, "SSH password for jumper@", 5*time.Second)
	keys(scr, "tunnel-pw", tcell.KeyEnter)
	waitFor(t, a, scr, "via SSH tunnelh", 10*time.Second) // saved: listed in F2

	data, _ := os.ReadFile(cfg.Path)
	text := string(data)
	for _, want := range []string{"[profiles.tun.ssh]", "alias = 'tunnelhost'", "user = 'jumper'", "port = " + sp, "password = 'stored'"} {
		if !strings.Contains(strings.ReplaceAll(text, `"`, "'"), want) {
			t.Errorf("config lacks %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "tunnel-pw") || strings.Contains(text, os.Getenv("DBCC_IT_PW")) {
		t.Fatalf("a secret went into config.toml:\n%s", text)
	}
	if kind, sec, ok, _ := config.StoredSSHSecret("tun"); !ok || sec != "tunnel-pw" || !strings.HasPrefix(kind, "password@") {
		t.Errorf("stored SSH secret %q %v", sec, ok)
	}
	if kh, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts")); !strings.Contains(string(kh), "["+sh+"]:"+sp) {
		t.Errorf("known_hosts: %q", kh)
	}

	// Connecting now asks nothing: stored secret, known host key.
	logins := srv.Logins.Load()
	keys(scr, tcell.KeyEscape, tcell.KeyF9, "cd /tun", tcell.KeyEnter)
	lines = waitFor(t, a, scr, "information_schema", 10*time.Second)
	if all := strings.Join(lines, "\n"); strings.Contains(all, "SSH password") || strings.Contains(all, "authenticity") {
		t.Errorf("asked again:\n%s", all)
	}
	if srv.Logins.Load() != logins+1 {
		t.Errorf("logins %d → %d", logins, srv.Logins.Load())
	}
	if p := a.cfg.Profiles["tun"]; p == nil || p.SSH == nil || p.SSH.Port != mustAtoi(sp) {
		t.Errorf("profile in memory: %+v", p)
	}
}

func mustAtoi(s string) int { n, _ := strconv.Atoi(s); return n }

// TestHostKeyDialogTabDoesNotWrap: from Cancel, Tab/Right never reach
// "Trust and connect"; only Left/Shift+Tab do.
func TestHostKeyDialogTabDoesNotWrap(t *testing.T) {
	a, scr, stop := startApp(t, emptyConfig(), Options{NoSplash: true})
	defer stop()
	waitFor(t, a, scr, "1Help", 5*time.Second)
	res := make(chan bool, 1)
	go func() { ok, _ := a.confirmHostKey("The authenticity of host x can't be established."); res <- ok }()
	waitFor(t, a, scr, "Trust and connect", 3*time.Second)
	keys(scr, tcell.KeyTab, tcell.KeyRight, tcell.KeyTab, tcell.KeyEnter)
	select {
	case ok := <-res:
		if ok {
			t.Fatal("Tab/Right then Enter trusted the key")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answer")
	}
	go func() { ok, _ := a.confirmHostKey("The authenticity of host x can't be established."); res <- ok }()
	waitFor(t, a, scr, "Trust and connect", 3*time.Second)
	keys(scr, tcell.KeyLeft, tcell.KeyLeft, tcell.KeyEnter)
	if ok := <-res; !ok {
		t.Fatal("Left, Enter did not trust")
	}
}
