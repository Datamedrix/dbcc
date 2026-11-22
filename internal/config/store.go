package config

// Saving profiles from the UI and the credential store.
//
// Owner decision (A22, 2026-11-21): connection credentials are stored too.
// They live in a separate file, CredentialsPath() (mode 0600), never in
// config.toml, so the profile file stays free of secrets. A profile uses
// them with password = "stored".

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// CredentialsPath is the credential store file.
func CredentialsPath() string { return filepath.Join(ConfigDir(), "credentials.toml") }

var storeMu sync.Mutex

type credFile struct {
	Profiles map[string]storedCred `toml:"profiles"`
}

type storedCred struct {
	Password    *string `toml:"password,omitempty"`     // nil: no database password stored
	SSHPassword string  `toml:"ssh_password,omitempty"` // SSH password or key passphrase (A27)
	// SSHSecretKind says which question SSHPassword answers: "password"
	// (the target's SSH password) or "passphrase:<key file>".
	SSHSecretKind string `toml:"ssh_secret_kind,omitempty"`
}

func readCreds(path string) (credFile, error) {
	cf := credFile{Profiles: map[string]storedCred{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cf, nil
	}
	if err != nil {
		return cf, err
	}
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return cf, fmt.Errorf("credential store %s has permissions %04o and cannot be restricted: %w", path, st.Mode().Perm(), err)
		}
	}
	if err := toml.Unmarshal(data, &cf); err != nil {
		return cf, fmt.Errorf("parse %s: %w", path, err)
	}
	if cf.Profiles == nil {
		cf.Profiles = map[string]storedCred{}
	}
	return cf, nil
}

func writeCreds(path string, cf credFile) error {
	var b bytes.Buffer
	b.WriteString("# dbcc credential store — keep private (mode 0600).\n\n")
	names := make([]string, 0, len(cf.Profiles))
	for n := range cf.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sec, err := toml.Marshal(map[string]any{"profiles": map[string]any{n: cf.Profiles[n]}})
		if err != nil {
			return err
		}
		b.WriteString(stripParentHeader(string(sec)))
		b.WriteString("\n")
	}
	return writeFileAtomic(path, b.Bytes(), 0o600)
}

// StoredPassword returns the stored password of a profile.
func StoredPassword(profile string) (string, bool, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	cf, err := readCreds(CredentialsPath())
	if err != nil {
		return "", false, err
	}
	c, ok := cf.Profiles[profile]
	if !ok || c.Password == nil {
		return "", false, nil
	}
	return *c.Password, true, nil
}

// StorePassword saves the password of a profile in the credential store.
func StorePassword(profile, password string) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := CredentialsPath()
	cf, err := readCreds(path)
	if err != nil {
		return err
	}
	c := cf.Profiles[profile]
	c.Password = &password
	cf.Profiles[profile] = c
	return writeCreds(path, cf)
}

// DeletePassword removes a profile's stored database password (no error
// if absent); a stored SSH secret stays.
func DeletePassword(profile string) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := CredentialsPath()
	cf, err := readCreds(path)
	if err != nil {
		return err
	}
	c, ok := cf.Profiles[profile]
	if !ok || c.Password == nil {
		return nil
	}
	c.Password = nil
	if c.SSHPassword == "" {
		delete(cf.Profiles, profile)
	} else {
		cf.Profiles[profile] = c
	}
	return writeCreds(path, cf)
}

// deleteCredentials removes everything stored for a profile.
func deleteCredentials(profile string) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := CredentialsPath()
	cf, err := readCreds(path)
	if err != nil {
		return err
	}
	if _, ok := cf.Profiles[profile]; !ok {
		return nil
	}
	delete(cf.Profiles, profile)
	return writeCreds(path, cf)
}

// StoredSSHSecret returns the stored SSH password or key passphrase of a
// profile (A27) and which question it answers (see storedCred).
func StoredSSHSecret(profile string) (kind, secret string, ok bool, err error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	cf, err := readCreds(CredentialsPath())
	if err != nil {
		return "", "", false, err
	}
	c, ok := cf.Profiles[profile]
	if !ok || c.SSHPassword == "" || c.SSHSecretKind == "" {
		return "", "", false, nil
	}
	return c.SSHSecretKind, c.SSHPassword, true, nil
}

// StoreSSHSecret saves the SSH password or key passphrase of a profile
// with its kind ("password" or "passphrase:<key file>"); an empty secret
// removes it.
func StoreSSHSecret(profile, kind, secret string) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	path := CredentialsPath()
	cf, err := readCreds(path)
	if err != nil {
		return err
	}
	c, ok := cf.Profiles[profile]
	if !ok && secret == "" {
		return nil
	}
	c.SSHPassword, c.SSHSecretKind = secret, kind
	if secret == "" {
		c.SSHSecretKind = ""
	}
	if c.Password == nil && c.SSHPassword == "" {
		delete(cf.Profiles, profile)
	} else {
		cf.Profiles[profile] = c
	}
	return writeCreds(path, cf)
}

// ValidProfileName reports whether name can be used as a profile name
// (it is also the first element of paths: /name/db).
func ValidProfileName(name string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`).MatchString(name)
}

// SaveProfile writes p into the config file at path, replacing an existing
// [profiles.<name>] section (and its sub-tables) and keeping everything
// else — comments, settings, other profiles — untouched. The profile is
// validated first. It also updates c.Profiles when c is non-nil.
func SaveProfile(c *Config, path string, p *Profile) error {
	if !ValidProfileName(p.Name) {
		return fmt.Errorf("invalid profile name %q: use letters, digits, '-', '_' or '.'", p.Name)
	}
	cp := *p
	if err := validateProfile(&cp); err != nil {
		return err
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := removeProfileSection(string(data), p.Name)
	sec, err := toml.Marshal(map[string]any{"profiles": map[string]any{p.Name: profileTOML(p)}})
	if err != nil {
		return err
	}
	if text != "" && !strings.HasSuffix(text, "\n\n") {
		text = strings.TrimRight(text, "\n") + "\n\n"
	}
	text += stripParentHeader(string(sec))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(path, []byte(text), 0o600); err != nil {
		return err
	}
	if c != nil {
		if c.Profiles == nil {
			c.Profiles = map[string]*Profile{}
		}
		c.Profiles[p.Name] = &cp
		if c.Path == "" {
			c.Path = path
		}
	}
	return nil
}

// DeleteProfile removes a profile from the config file and its stored
// password from the credential store.
func DeleteProfile(c *Config, path, name string) error {
	storeMu.Lock()
	data, err := os.ReadFile(path)
	if err == nil {
		err = writeFileAtomic(path, []byte(removeProfileSection(string(data), name)), 0o600)
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	storeMu.Unlock()
	if err != nil {
		return err
	}
	if c != nil {
		delete(c.Profiles, name)
	}
	return deleteCredentials(name)
}

// profileTOML returns the profile as an ordered set of TOML keys, leaving
// out empty optional values.
func profileTOML(p *Profile) map[string]any {
	m := map[string]any{"driver": p.Driver}
	if p.Host != "" {
		m["host"] = p.Host
	}
	if p.Port != 0 {
		m["port"] = p.Port
	}
	if p.Socket != "" {
		m["socket"] = p.Socket
	}
	if p.User != "" {
		m["user"] = p.User
	}
	if p.Database != "" {
		m["database"] = p.Database
	}
	if p.Password != "" {
		m["password"] = p.Password
	}
	if p.Autocommit != nil {
		m["autocommit"] = *p.Autocommit
	}
	if p.ReadOnly {
		m["read_only"] = true
	}
	if p.ConfirmWrites {
		m["confirm_writes"] = true
	}
	if len(p.Params) > 0 {
		m["params"] = p.Params
	}
	if s := p.SSH; s != nil {
		t := map[string]any{}
		for k, v := range map[string]string{"alias": s.Alias, "host": s.Host, "user": s.User,
			"identity_file": s.IdentityFile, "proxy_jump": s.ProxyJump, "password": s.Password} {
			if v != "" {
				t[k] = v
			}
		}
		if s.Port != 0 {
			t["port"] = s.Port
		}
		m["ssh"] = t
	}
	return m
}

// removeProfileSection deletes the [profiles.<name>] table and its
// sub-tables ([profiles.<name>.params]) from TOML text, plus the blank
// lines directly before it.
func removeProfileSection(text, name string) string {
	lines := strings.SplitAfter(text, "\n")
	header := regexp.MustCompile(`^\s*\[\s*profiles\s*\.\s*(?:"([^"]*)"|'([^']*)'|([A-Za-z0-9_-]+))\s*(\.[^\]]*)?\]\s*(#.*)?$`)
	anyHeader := regexp.MustCompile(`^\s*\[`)
	var out []string
	skipping := false
	for _, l := range lines {
		trim := strings.TrimRight(l, "\r\n")
		if anyHeader.MatchString(trim) {
			skipping = false
			if m := header.FindStringSubmatch(trim); m != nil {
				n := m[1] + m[2] + m[3]
				if n == name {
					skipping = true
					// drop blank lines that separated this section
					for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
						out = out[:len(out)-1]
					}
					continue
				}
			}
		}
		if !skipping {
			out = append(out, l)
		}
	}
	res := strings.Join(out, "")
	if res != "" && !strings.HasSuffix(res, "\n") {
		res += "\n"
	}
	return res
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dbcc-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// stripParentHeader removes the bare "[profiles]" table header that the
// TOML encoder emits before a [profiles.<name>] section.
func stripParentHeader(sec string) string {
	return strings.TrimPrefix(sec, "[profiles]\n")
}
