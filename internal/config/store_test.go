package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveProfileKeepsRestAndStoresPassword(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "dbcc", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	orig := "# my comment\n[settings]\nmouse = true\n\n[profiles.a]\ndriver = \"mysql\"\nhost = \"h\"\n\n[profiles.a.params]\ntls = \"true\"\n\n[profiles.b]\ndriver = \"postgres\"\nhost = \"x\"\n"
	os.WriteFile(path, []byte(orig), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &Profile{Name: "a", Driver: "mysql", Host: "new", Port: 3307, User: "u", Password: "stored"}
	if err := SaveProfile(cfg, path, p); err != nil {
		t.Fatal(err)
	}
	if err := StorePassword("a", "s3cret"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, want := range []string{"# my comment", "mouse = true", "[profiles.b]", "host = 'new'", "password = 'stored'"} {
		if !strings.Contains(text, want) {
			t.Errorf("config lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "tls") || strings.Contains(text, "s3cret") || strings.Contains(text, "\n[profiles]\n") {
		t.Errorf("old section, password or bare header left in config:\n%s", text)
	}
	cfg2, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg2.Profiles["a"].Host != "new" || cfg2.Profiles["b"] == nil {
		t.Errorf("reload: %+v", cfg2.Profiles)
	}
	cred, err := ResolveCredentials(cfg2.Profiles["a"], nil)
	if err != nil || cred.Password != "s3cret" {
		t.Fatalf("stored password: %q, %v", cred.Password, err)
	}
	st, _ := os.Stat(CredentialsPath())
	if st.Mode().Perm() != 0o600 {
		t.Errorf("credential store mode %o", st.Mode().Perm())
	}
	if err := DeleteProfile(cfg2, path, "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := StoredPassword("a"); ok {
		t.Error("password survived DeleteProfile")
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("reload after delete: %v", err)
	}
}

func TestStoredWithoutEntryPrompts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := &Profile{Name: "z", Driver: "mysql", Password: "stored"}
	cred, err := ResolveCredentials(p, func(string) (string, error) { return "pw", nil })
	if err != nil || cred.Password != "pw" || !cred.PromptedForStore {
		t.Fatalf("got %+v, %v", cred, err)
	}
}
