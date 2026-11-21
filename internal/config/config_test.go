package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultsMissingFile(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Settings != DefaultSettings() || c.Path != "" {
		t.Fatalf("%+v", c)
	}
	d := DefaultSettings()
	if d.FetchSize != 500 || d.MaxRows != 100000 || d.MaxRowsBuffered != 10000 || d.MaxColumnWidth != 40 ||
		d.PivotColumnWidth != 30 || d.CompletionKeyAlt != "ctrl+o" || d.Mouse || !d.Splash || d.PageSize != 3000 {
		t.Fatalf("%+v", d)
	}
}

func TestPartialSettings(t *testing.T) {
	p := write(t, "c.toml", "[settings]\nfetch_size = 7\nsplash = false\n", 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Settings.FetchSize != 7 || c.Settings.Splash || c.Settings.MaxRows != 100000 || c.Settings.CompletionKeyAlt != "ctrl+o" {
		t.Fatalf("%+v", c.Settings)
	}
	if len(c.Warnings) != 0 {
		t.Fatal(c.Warnings)
	}
}

func TestPageSizeSetting(t *testing.T) {
	c, err := Load(write(t, "c.toml", "[settings]\npage_size = 250\n", 0o600))
	if err != nil || c.Settings.PageSize != 250 {
		t.Fatalf("%v %+v", err, c)
	}
	for _, bad := range []string{"9", "0", "-1"} {
		if _, err := Load(write(t, "c.toml", "[settings]\npage_size = "+bad+"\n", 0o600)); err == nil ||
			!strings.Contains(err.Error(), "page_size") {
			t.Errorf("page_size = %s: %v", bad, err)
		}
	}
}

func TestProfilesAndKeys(t *testing.T) {
	p := write(t, "c.toml", `
[profiles.shop-dev]
driver = "mysql"
host = "h"
user = "u"
password = "env:X"
autocommit = false
[profiles.pg]
driver = "postgres"
socket = "/tmp/s"
password = "pgpass"
[keys.grid]
pivot = "f3"
`, 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := c.Profile("shop-dev")
	if err != nil || sd.Name != "shop-dev" || sd.Port != 3306 || sd.IsAutocommit() {
		t.Fatalf("%+v %v", sd, err)
	}
	pg, _ := c.Profile("pg")
	if pg.Port != 0 {
		t.Fatalf("port defaulted with socket: %d", pg.Port)
	}
	if c.Keys["grid"]["pivot"] != "f3" {
		t.Fatal(c.Keys)
	}
	if _, err := c.Profile("zzz"); err == nil {
		t.Fatal("want error")
	}
}

func TestDefaultPorts(t *testing.T) {
	p := write(t, "c.toml", "[profiles.a]\ndriver=\"postgres\"\n[profiles.b]\ndriver=\"mssql\"\n", 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Profiles["a"].Port != 5432 || c.Profiles["b"].Port != 1433 {
		t.Fatal("ports")
	}
}

func TestLiteralPasswordRejected(t *testing.T) {
	p := write(t, "c.toml", "[profiles.a]\ndriver=\"mysql\"\npassword=\"hunter2\"\n", 0o600)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "passwords must not be stored in the profile file") {
		t.Fatal(err)
	}
}

func TestInvalidDriver(t *testing.T) {
	p := write(t, "c.toml", "[profiles.a]\ndriver=\"oracle\"\n", 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("want error")
	}
}

func TestPermissionWarning(t *testing.T) {
	p := write(t, "c.toml", "", 0o644)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 1 || c.Path != p {
		t.Fatal(c.Warnings)
	}
}

func TestXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/c")
	t.Setenv("XDG_STATE_HOME", "/x/s")
	if ConfigDir() != "/x/c/dbcc" || StateDir() != "/x/s/dbcc" || DefaultPath() != "/x/c/dbcc/config.toml" {
		t.Fatal(ConfigDir(), StateDir())
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/h")
	if ConfigDir() != "/h/.config/dbcc" || StateDir() != "/h/.local/state/dbcc" {
		t.Fatal(ConfigDir(), StateDir())
	}
}

func TestReadMyCnf(t *testing.T) {
	p := write(t, "my.cnf", `# c
!include /etc/x
[mysqld]
user = nope
[client]
user=anton ; not a comment char inline for ;
password = "pa ss#word"
host = 'db.example'
port = 3307 # trailing
; comment
socket=/tmp/m.sock
[mysql]
user = other
`, 0o600)
	m, err := ReadMyCnf(p)
	if err != nil {
		t.Fatal(err)
	}
	if m["password"] != "pa ss#word" || m["host"] != "db.example" || m["port"] != "3307" || m["socket"] != "/tmp/m.sock" {
		t.Fatal(m)
	}
	if !strings.HasPrefix(m["user"], "anton") {
		t.Fatal(m)
	}
}

func TestPgpass(t *testing.T) {
	p := write(t, ".pgpass", `# comment
db1:5432:shop:bob:first
*:*:shop:*:wild
h\:x:5432:*:*:co\:lon\\pw
*:*:*:*:last
`, 0o600)
	cases := []struct {
		host string
		port int
		db   string
		user string
		pw   string
		ok   bool
	}{
		{"db1", 5432, "shop", "bob", "first", true},
		{"db2", 1, "shop", "x", "wild", true},
		{"h:x", 5432, "any", "u", `co:lon\pw`, true},
		{"z", 1, "other", "u", "last", true},
	}
	for _, c := range cases {
		pw, ok, err := LookupPgpass(p, c.host, c.port, c.db, c.user)
		if err != nil || ok != c.ok || pw != c.pw {
			t.Fatalf("%+v -> %q %v %v", c, pw, ok, err)
		}
	}
	if _, ok, _ := LookupPgpass(filepath.Join(t.TempDir(), "none"), "a", 1, "d", "u"); ok {
		t.Fatal("missing file")
	}
}

func TestResolveCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PGPASSFILE", "")
	t.Setenv("DBCC_T_PW", "envpw")
	os.Unsetenv("DBCC_T_UNSET")

	p := &Profile{Name: "n", Driver: "mysql", Host: "h", Port: 1, User: "u", Password: "env:DBCC_T_PW"}
	c, err := ResolveCredentials(p, nil)
	if err != nil || c.Password != "envpw" || c.Host != "h" {
		t.Fatal(c, err)
	}
	p.Password = "env:DBCC_T_UNSET"
	if _, err := ResolveCredentials(p, nil); err == nil {
		t.Fatal("want error")
	}

	p.Password = "prompt"
	if _, err := ResolveCredentials(p, nil); err == nil {
		t.Fatal("want error nil prompter")
	}
	var got string
	c, err = ResolveCredentials(p, func(s string) (string, error) { got = s; return "pp", nil })
	if err != nil || c.Password != "pp" || got != "Password for n: " {
		t.Fatal(c, err, got)
	}

	os.WriteFile(filepath.Join(home, ".my.cnf"), []byte("[client]\nuser=cu\npassword=cp\nhost=ch\nport=3399\n"), 0o600)
	p = &Profile{Name: "n", Driver: "mysql", User: "mine", Password: "mycnf"}
	c, err = ResolveCredentials(p, nil)
	if err != nil || c.User != "mine" || c.Password != "cp" || c.Host != "ch" || c.Port != 3399 {
		t.Fatal(c, err)
	}

	os.WriteFile(filepath.Join(home, ".pgpass"), []byte("h:5432:d:u:pgpw\n"), 0o600)
	p = &Profile{Name: "n", Driver: "postgres", Host: "h", Port: 5432, Database: "d", User: "u", Password: "pgpass"}
	c, err = ResolveCredentials(p, nil)
	if err != nil || c.Password != "pgpw" {
		t.Fatal(c, err)
	}
	p.User = "other"
	if _, err = ResolveCredentials(p, nil); err == nil {
		t.Fatal("want no match error")
	}
}
