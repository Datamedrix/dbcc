package session

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dbcc/internal/config"
	"dbcc/internal/sshtunnel/sshtest"
)

func sshEnv(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("SSH_AUTH_SOCK", "")
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	old := SSHConfigPath
	SSHConfigPath = func() string { return filepath.Join(home, ".ssh", "config") }
	t.Cleanup(func() { SSHConfigPath = old })
	return home
}

func hostPort(addr string) (string, int) {
	h, p, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	return h, n
}

// A stored SSH secret answers only the target's question of its kind: it
// is never sent to a jump host, and a stored passphrase is never sent as
// a password.
func TestStoredSSHSecretOnlyForItsQuestion(t *testing.T) {
	home := sshEnv(t)
	jump := sshtest.Start(t, sshtest.Options{Password: "jump-pw"})
	target := sshtest.Start(t, sshtest.Options{Password: "target-pw"})
	jh, jp := hostPort(jump.Addr)
	th, tp := hostPort(target.Addr)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(
		"Host *\n  StrictHostKeyChecking accept-new\nHost bastion\n  HostName "+jh+"\n  Port "+strconv.Itoa(jp)+"\n  User j\n"), 0o600)
	p := &config.Profile{Name: "tun", Driver: "mysql",
		SSH: &config.SSH{Host: th, Port: tp, User: "t", ProxyJump: "bastion", Password: "stored"}}
	if err := config.StoreSSHSecret("tun", "password@"+target.Addr, "target-pw"); err != nil {
		t.Fatal(err)
	}
	var asked []string
	tu, err := OpenTunnel(context.Background(), p, Hooks{Prompt: func(prompt string) (string, error) {
		asked = append(asked, prompt)
		return "jump-pw", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	tu.Close()
	// The jump host was asked (not given the stored target password); the
	// target got the stored one.
	if len(asked) != 1 || !strings.Contains(asked[0], "j@"+jump.Addr) {
		t.Errorf("prompts: %q", asked)
	}

	// A stored passphrase is not an answer to a password question.
	config.StoreSSHSecret("tun", "passphrase:/home/x/.ssh/key@"+target.Addr, "target-pw")
	asked = nil
	tu, err = OpenTunnel(context.Background(), p, Hooks{Prompt: func(prompt string) (string, error) {
		asked = append(asked, prompt)
		if strings.Contains(prompt, "j@") {
			return "jump-pw", nil
		}
		return "target-pw", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	tu.Close()
	if len(asked) != 2 {
		t.Errorf("the stored passphrase was used as a password: prompts %q", asked)
	}
	// What worked is stored with its kind — the target's password, not
	// the jump host's.
	if k, s, ok, _ := config.StoredSSHSecret("tun"); !ok || k != "password@"+target.Addr || s != "target-pw" {
		t.Errorf("stored: %q %q %v", k, s, ok)
	}
	// NoStore (the editor's test) stores nothing but reports the secret.
	config.StoreSSHSecret("tun", "", "")
	var gotKind, gotSecret string
	tu, err = OpenTunnel(context.Background(), p, Hooks{NoStore: true,
		SecretUsed: func(k, s string) { gotKind, gotSecret = k, s },
		Prompt: func(prompt string) (string, error) {
			if strings.Contains(prompt, "j@") {
				return "jump-pw", nil
			}
			return "target-pw", nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	tu.Close()
	if _, _, ok, _ := config.StoredSSHSecret("tun"); ok || gotKind != "password@"+target.Addr || gotSecret != "target-pw" {
		t.Errorf("NoStore: stored %v, reported %q %q", ok, gotKind, gotSecret)
	}
}

// A secret stored for one SSH server is never offered to another (the
// SSH host of the profile was changed, in the editor or by hand).
func TestStoredSecretBoundToServer(t *testing.T) {
	home := sshEnv(t)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host *\n  StrictHostKeyChecking accept-new\n"), 0o600)
	a := sshtest.Start(t, sshtest.Options{Password: "pw-a"})
	b := sshtest.Start(t, sshtest.Options{Password: "pw-b"})
	config.StoreSSHSecret("tun", "password@"+a.Addr, "pw-a")
	bh, bp := hostPort(b.Addr)
	p := &config.Profile{Name: "tun", Driver: "mysql", SSH: &config.SSH{Host: bh, Port: bp, User: "u", Password: "stored"}}
	var asked int
	tu, err := OpenTunnel(context.Background(), p, Hooks{NoStore: true, Prompt: func(string) (string, error) {
		asked++
		return "pw-b", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	tu.Close()
	if asked != 1 {
		t.Errorf("server B was not asked (got A's stored password?): %d prompts", asked)
	}
}

// Without an alias, ssh.host is looked up like a name given to ssh: a
// Host of ~/.ssh/config gets its HostName.
func TestManualHostIsLookedUp(t *testing.T) {
	home := sshEnv(t)
	os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host prod\n  HostName 10.0.0.5\n  User deploy\n"), 0o600)
	tg, err := SSHTarget(&config.Profile{Name: "x", SSH: &config.SSH{Host: "prod"}})
	if err != nil || tg.Host != "10.0.0.5" || tg.User != "deploy" {
		t.Errorf("manual alias: %+v %v", tg, err)
	}
	// With an alias, the saved host wins (taken over at save time).
	tg, _ = SSHTarget(&config.Profile{Name: "x", SSH: &config.SSH{Alias: "prod", Host: "10.0.0.9"}})
	if tg.Host != "10.0.0.9" || tg.User != "deploy" {
		t.Errorf("alias with saved host: %+v", tg)
	}
}

// Open never connects an SSH profile directly.
func TestOpenRefusesSSHProfileWithoutTunnel(t *testing.T) {
	p := &config.Profile{Name: "x", Driver: "mysql", Host: "127.0.0.1", SSH: &config.SSH{Host: "h"}}
	if _, err := Open(context.Background(), p, config.Credentials{Host: "127.0.0.1", Password: "secret"}, ""); err == nil ||
		!strings.Contains(err.Error(), "SSH tunnel") {
		t.Errorf("direct open: %v", err)
	}
}
