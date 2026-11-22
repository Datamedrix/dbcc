package sshtunnel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "local")
	os.MkdirAll(filepath.Join(home, ".ssh", "conf.d"), 0o700)
	cfgPath := filepath.Join(home, ".ssh", "config")
	writeFile(t, filepath.Join(home, ".ssh", "conf.d", "extra"), `
Host extra
  HostName extra.example
`)
	writeFile(t, cfgPath, `# comment
Include conf.d/*

Host bastion
  HostName 203.0.113.7
  User jump
  Port 2222

Host db-prod
  HostName=db.internal
  User "deploy"
  IdentityFile ~/.ssh/deploy_key
  IdentityFile %d/.ssh/second_%r
  IdentitiesOnly yes
  ProxyJump bastion
  HostKeyAlgorithms +ssh-rsa,ssh-dss
  KexAlgorithms +diffie-hellman-group1-sha1

Host *.internal !secret.internal
  ServerAliveInterval 10

Match host foo
  User ignored

Host *
  User fallback
  StrictHostKeyChecking accept-new
`)
	c, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Hosts(), " "); got != "extra bastion db-prod" {
		t.Errorf("hosts: %s", got)
	}
	tg, err := c.Resolve("db-prod")
	if err != nil {
		t.Fatal(err)
	}
	if tg.Host != "db.internal" || tg.User != "deploy" || tg.Port != 0 || !tg.IdentitiesOnly ||
		tg.StrictHostKeyChecking != "accept-new" || tg.HostKeyAlgorithms != "+ssh-rsa,ssh-dss" {
		t.Errorf("db-prod: %+v", tg)
	}
	// db-prod itself does not match *.internal: the pattern applies to the
	// name given on the command line, not to HostName.
	if tg.ServerAliveInterval != 0 {
		t.Errorf("ServerAliveInterval: %d", tg.ServerAliveInterval)
	}
	want := []string{filepath.Join(home, ".ssh", "deploy_key"), filepath.Join(home, ".ssh", "second_deploy")}
	if !slices.Equal(tg.IdentityFiles, want) {
		t.Errorf("identity files: %v", tg.IdentityFiles)
	}
	if len(tg.Jumps) != 1 || tg.Jumps[0].Host != "203.0.113.7" || tg.Jumps[0].Port != 2222 || tg.Jumps[0].User != "jump" {
		t.Errorf("jumps: %+v", tg.Jumps)
	}
	if tg.Addr() != "db.internal:22" || tg.String() != "db-prod (deploy@db.internal:22)" {
		t.Errorf("addr %s, string %s", tg.Addr(), tg.String())
	}
	// Patterns, negation and first-value-wins for an unlisted name.
	o, _ := c.Resolve("other.internal")
	if o.ServerAliveInterval != 10 || o.User != "fallback" || o.Host != "other.internal" {
		t.Errorf("other.internal: %+v", o)
	}
	s, _ := c.Resolve("secret.internal")
	if s.ServerAliveInterval != 0 {
		t.Errorf("negated pattern matched: %+v", s)
	}
	// Explicit user@host:port wins over the configuration.
	e, _ := c.Resolve("admin@bastion:2200")
	if e.User != "admin" || e.Port != 2200 || e.Host != "203.0.113.7" {
		t.Errorf("explicit: %+v", e)
	}
	if x, _ := c.Resolve("extra"); x.Host != "extra.example" {
		t.Errorf("include: %+v", x)
	}
	if _, err := c.Resolve("bad:port:x"); err != nil {
		t.Errorf("ipv6-like names are hosts: %v", err)
	}
	if _, err := c.Resolve("u@h:abc"); err == nil {
		t.Error("invalid port accepted")
	}
}

func TestProxyCommandRejected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	writeFile(t, p, "Host pc\n  ProxyCommand nc %h %p\n")
	c, _ := LoadConfig(p)
	if _, err := c.Resolve("pc"); err == nil || !strings.Contains(err.Error(), "ProxyJump") {
		t.Errorf("ProxyCommand: %v", err)
	}
}

func TestMissingConfigIsEmpty(t *testing.T) {
	c, err := LoadConfig(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(c.Hosts()) != 0 {
		t.Fatalf("%v %v", c, err)
	}
	tg, err := c.Resolve("me@example.org")
	if err != nil || tg.Host != "example.org" || tg.User != "me" {
		t.Errorf("%+v %v", tg, err)
	}
}

func TestApplyAlgoSpec(t *testing.T) {
	def := []string{"a", "b"}
	all := []string{"a", "b", "c", "d-1", "d-2"}
	for spec, want := range map[string]string{
		"c":    "c",
		"+c":   "a b c",
		"+d-*": "a b d-1 d-2",
		"-a":   "b",
		"^c":   "c a b",
		"x,b":  "b", // unknown names are dropped
		"+zzz": "a b",
		"^b,c": "b c a",
	} {
		if got := strings.Join(applyAlgoSpec(def, all, spec), " "); got != want {
			t.Errorf("%s: %s, want %s", spec, got, want)
		}
	}
}

// Include inside a Host block applies only to that host, and the block
// continues after the Include (OpenSSH semantics).
func TestIncludeInHostBlock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "common.conf"), "Host *\n  User shared\n  ProxyJump corp-bastion\n")
	cfg := filepath.Join(dir, "config")
	writeFile(t, cfg, "Host db\n  Include "+filepath.Join(dir, "common.conf")+"\n  HostName 10.1.1.1\n\nHost other\n  Port 2200\n")
	c, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db, err := c.Resolve("db")
	if err != nil {
		t.Fatal(err)
	}
	if db.Host != "10.1.1.1" || db.User != "shared" || len(db.Jumps) != 1 {
		t.Errorf("db: %+v", db)
	}
	o, err := c.Resolve("other")
	if err != nil {
		t.Fatal(err)
	}
	if o.Host != "other" || o.User != "" || len(o.Jumps) != 0 || o.Port != 2200 {
		t.Errorf("other got the included options of db: %+v", o)
	}
}

func TestStrictHostKeyCheckingValues(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	writeFile(t, p, "Host a\n StrictHostKeyChecking true\nHost b\n StrictHostKeyChecking false\nHost c\n StrictHostKeyChecking accept-new\nHost d\n UserKnownHostsFile /x/one\nHost *\n UserKnownHostsFile /x/two\n HostKeyAlias shared\n")
	c, _ := LoadConfig(p)
	for host, want := range map[string]string{"a": "yes", "b": "no", "c": "accept-new"} {
		if tg, _ := c.Resolve(host); tg.StrictHostKeyChecking != want {
			t.Errorf("%s: %q, want %q", host, tg.StrictHostKeyChecking, want)
		}
	}
	// UserKnownHostsFile: the first value only, like OpenSSH.
	if tg, _ := c.Resolve("d"); len(tg.KnownHosts) != 1 || tg.KnownHosts[0] != "/x/one" || tg.HostKeyAlias != "shared" {
		t.Errorf("d: %+v", tg)
	}
}
