package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

// Settings are the global UI/engine settings (spec section 15).
type Settings struct {
	FetchSize        int    `toml:"fetch_size"`
	MaxRows          int    `toml:"max_rows"` // kept for compatibility; the UI grid pages instead (PageSize)
	PageSize         int    `toml:"page_size"`
	MaxRowsBuffered  int    `toml:"max_rows_buffered"`
	MaxColumnWidth   int    `toml:"max_column_width"`
	PivotColumnWidth int    `toml:"pivot_column_width"`
	CompletionKeyAlt string `toml:"completion_key_alt"`
	Mouse            bool   `toml:"mouse"`
	Splash           bool   `toml:"splash"`
}

// DefaultSettings returns the built-in defaults.
func DefaultSettings() Settings {
	return Settings{
		FetchSize:        500,
		MaxRows:          100000,
		PageSize:         3000,
		MaxRowsBuffered:  10000,
		MaxColumnWidth:   40,
		PivotColumnWidth: 30,
		CompletionKeyAlt: "ctrl+o",
		Mouse:            false,
		Splash:           true,
	}
}

// Config is the loaded configuration.
type Config struct {
	Settings Settings
	Profiles map[string]*Profile
	Keys     map[string]map[string]string
	Path     string
	Warnings []string
}

type rawConfig struct {
	Settings Settings                     `toml:"settings"`
	Profiles map[string]*Profile          `toml:"profiles"`
	Keys     map[string]map[string]string `toml:"keys"`
}

// HomeDir returns the user's home directory (os.UserHomeDir, fallback $HOME).
func HomeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

// ConfigDir returns $XDG_CONFIG_HOME/dbcc or ~/.config/dbcc.
func ConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "dbcc")
	}
	return filepath.Join(HomeDir(), ".config", "dbcc")
}

// StateDir returns $XDG_STATE_HOME/dbcc or ~/.local/state/dbcc.
func StateDir() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "dbcc")
	}
	return filepath.Join(HomeDir(), ".local", "state", "dbcc")
}

// DefaultPath is the default config file location.
func DefaultPath() string { return filepath.Join(ConfigDir(), "config.toml") }

var defaultPorts = map[string]int{"mysql": 3306, "postgres": 5432, "mssql": 1433}

// Load reads and validates the config file. A missing file yields defaults.
func Load(path string) (*Config, error) {
	cfg := &Config{
		Settings: DefaultSettings(),
		Profiles: map[string]*Profile{},
		Keys:     map[string]map[string]string{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg.Path = path
	if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf(
			"config file %s has permissions %04o; expected 0600 or stricter", path, st.Mode().Perm()))
	}
	raw := rawConfig{Settings: cfg.Settings}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.Settings = raw.Settings
	if err := validateSettings(cfg.Settings); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if raw.Keys != nil {
		cfg.Keys = raw.Keys
	}
	names := make([]string, 0, len(raw.Profiles))
	for n := range raw.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := raw.Profiles[n]
		if p == nil {
			p = &Profile{}
		}
		p.Name = n
		if err := validateProfile(p); err != nil {
			return nil, err
		}
		cfg.Profiles[n] = p
	}
	return cfg, nil
}

// MinPageSize is the smallest accepted page_size.
const MinPageSize = 10

func validateSettings(s Settings) error {
	if s.PageSize < MinPageSize {
		return fmt.Errorf("settings.page_size = %d: must be at least %d", s.PageSize, MinPageSize)
	}
	return nil
}

func validateProfile(p *Profile) error {
	def, ok := defaultPorts[p.Driver]
	if !ok {
		return fmt.Errorf("profile %q: invalid driver %q (want mysql, postgres or mssql)", p.Name, p.Driver)
	}
	if !validPasswordSource(p.Password) {
		return fmt.Errorf("profile %q: passwords must not be stored in the profile file; use password = \"prompt\", \"stored\" (credential store), \"env:NAME\", \"mycnf\" or \"pgpass\"", p.Name)
	}
	if p.Port == 0 && p.Socket == "" {
		p.Port = def
	}
	return nil
}

func validPasswordSource(s string) bool {
	switch s {
	case "", "prompt", "mycnf", "pgpass", "stored":
		return true
	}
	return len(s) > 4 && s[:4] == "env:"
}

// Profile returns the named profile.
func (c *Config) Profile(name string) (*Profile, error) {
	p, ok := c.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", name)
	}
	return p, nil
}
