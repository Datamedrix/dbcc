package config

import (
	"context"
	"net"
	"strconv"
)

// Profile is one connection profile from config.toml (spec section 15).
// It never contains a password; Password holds the *source* of the password.
type Profile struct {
	Name          string `toml:"-"`
	Driver        string `toml:"driver"` // mysql | postgres | mssql
	Host          string `toml:"host"`
	Port          int    `toml:"port"`
	Socket        string `toml:"socket"`
	User          string `toml:"user"`
	Database      string `toml:"database"`
	Password      string `toml:"password"` // prompt | stored | env:VAR | mycnf | pgpass
	Autocommit    *bool  `toml:"autocommit"`
	ReadOnly      bool   `toml:"read_only"`
	ConfirmWrites bool   `toml:"confirm_writes"`
	// Params are extra driver parameters (e.g. tls, sslmode, encrypt).
	Params map[string]string `toml:"params"`
	// SSH, if set, connects through an SSH tunnel (A27). Host and Port of
	// the profile are then the database address as seen from the SSH
	// server (often 127.0.0.1).
	SSH *SSH `toml:"ssh"`
}

// SSH is the SSH tunnel of a profile (A27). Alias names a Host of
// ~/.ssh/config; its options (algorithms, known hosts, ProxyJump …) apply.
// The other fields were taken over from the configuration when the
// connection was tested and saved, so the profile still works when the
// alias is gone; a non-empty field overrides the configuration.
type SSH struct {
	Alias        string `toml:"alias"`
	Host         string `toml:"host"`
	Port         int    `toml:"port"`
	User         string `toml:"user"`
	IdentityFile string `toml:"identity_file"`
	ProxyJump    string `toml:"proxy_jump"`
	// Password is where an SSH password or key passphrase comes from:
	// "prompt" (default; agent and unencrypted keys need none) or "stored"
	// (credential store, saved after the first successful connection).
	Password string `toml:"password"`
}

// Name is the alias, else user@host:port, for messages.
func (s *SSH) Name() string {
	if s == nil {
		return ""
	}
	if s.Alias != "" {
		return s.Alias
	}
	n := s.Host
	if s.User != "" {
		n = s.User + "@" + n
	}
	if s.Port != 0 && s.Port != 22 {
		n += ":" + strconv.Itoa(s.Port)
	}
	return n
}

// IsAutocommit reports the effective autocommit setting (default true).
func (p *Profile) IsAutocommit() bool { return p.Autocommit == nil || *p.Autocommit }

// Credentials are the resolved connection credentials of a profile.
type Credentials struct {
	Host     string
	Port     int
	Socket   string
	User     string
	Password string
	// Dial, if set, opens the network connections to the database (the
	// SSH tunnel, A27). The address is host:port as seen from the dialler.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// PromptedForStore is set when the profile uses the credential store
	// but nothing was stored, so the password was prompted for.
	PromptedForStore bool
}
