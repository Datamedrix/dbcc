package config

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
	// PromptedForStore is set when the profile uses the credential store
	// but nothing was stored, so the password was prompted for.
	PromptedForStore bool
}
