package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadSSHProfileAndSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(ConfigDir(), "config.toml")
	p := &Profile{Name: "tun", Driver: "postgres", Host: "127.0.0.1", Port: 5432, User: "app", Password: "stored",
		SSH: &SSH{Alias: "bastion", Host: "203.0.113.7", Port: 2222, User: "jump", IdentityFile: "~/.ssh/k", ProxyJump: "edge", Password: "stored"}}
	if err := SaveProfile(nil, path, p); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := c.Profiles["tun"]
	if got == nil || got.SSH == nil || *got.SSH != *p.SSH {
		t.Fatalf("round trip: %+v", got)
	}
	// Secrets: DB password and SSH secret are independent.
	if err := StorePassword("tun", "dbpw"); err != nil {
		t.Fatal(err)
	}
	if err := StoreSSHSecret("tun", "password", "sshpw"); err != nil {
		t.Fatal(err)
	}
	if err := DeletePassword("tun"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := StoredPassword("tun"); ok {
		t.Error("DB password still stored")
	}
	if k, s, ok, _ := StoredSSHSecret("tun"); !ok || s != "sshpw" || k != "password" {
		t.Errorf("SSH secret lost with the DB password: %q %v", s, ok)
	}
	// An empty stored DB password is still "stored" (distinct from none).
	StorePassword("tun", "")
	if pw, ok, _ := StoredPassword("tun"); !ok || pw != "" {
		t.Errorf("empty password: %q %v", pw, ok)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "sshpw") || strings.Contains(string(data), "dbpw") {
		t.Fatalf("secret in config.toml:\n%s", data)
	}
	// Deleting the profile removes everything.
	if err := DeleteProfile(nil, path, "tun"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := StoredSSHSecret("tun"); ok {
		t.Error("SSH secret survived DeleteProfile")
	}
	// Invalid: secrets in the file, sockets through SSH.
	bad := *p
	bad.SSH = &SSH{Host: "h", Password: "hunter2"}
	if err := SaveProfile(nil, path, &bad); err == nil {
		t.Error("ssh.password with a secret accepted")
	}
	bad.SSH = &SSH{Host: "h"}
	bad.Socket = "/run/pg"
	if err := SaveProfile(nil, path, &bad); err == nil {
		t.Error("socket through SSH accepted")
	}
}
