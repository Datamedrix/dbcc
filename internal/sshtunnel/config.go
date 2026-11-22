// Package sshtunnel connects database sessions through an SSH server: it
// reads the OpenSSH client configuration (~/.ssh/config), authenticates
// (agent, key files, password), checks host keys against known_hosts and
// dials the database address on the far side of the SSH connection.
package sshtunnel

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is a parsed OpenSSH client configuration. It supports the subset
// that matters for tunnelling: Host blocks with patterns (*, ?, !),
// Include, and first-obtained-value-wins semantics. Match blocks are not
// evaluated; their options are ignored.
type Config struct {
	blocks []block
}

type block struct {
	patterns []string // empty: global options before the first Host
	match    bool     // a Match block: never applies
	// conds are the patterns of the Host blocks that Include-d this one:
	// all must match too (OpenSSH: the content of an Include in a block
	// that does not match never applies).
	conds [][]string
	never bool // included from a Match block
	opts  []option
}

type option struct {
	key, value string // key lower-case
}

// DefaultConfigPath is ~/.ssh/config.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ssh", "config")
}

// LoadConfig reads an ssh client configuration. A missing file is an
// empty configuration.
func LoadConfig(path string) (*Config, error) {
	c := &Config{}
	if err := c.load(path, 0); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) load(path string, depth int) error {
	return c.loadIn(path, depth, block{})
}

// loadIn reads path; parent is the block the Include stands in (zero for
// the top-level file).
func (c *Config) loadIn(path string, depth int, parent block) error {
	if depth > 8 {
		return fmt.Errorf("%s: Include nested too deeply", path)
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) && depth == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	// Options before the first Host of an included file belong to the
	// including block.
	inherited := parent.conds
	if len(parent.patterns) > 0 {
		inherited = append(append([][]string(nil), parent.conds...), parent.patterns)
	}
	never := parent.never || parent.match
	c.blocks = append(c.blocks, block{conds: inherited, never: never})
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		key, args := splitLine(sc.Text())
		if key == "" {
			continue
		}
		switch key {
		case "host":
			if len(args) == 0 {
				return fmt.Errorf("%s:%d: Host without pattern", path, line)
			}
			c.blocks = append(c.blocks, block{patterns: args, conds: inherited, never: never})
		case "match":
			c.blocks = append(c.blocks, block{match: true, conds: inherited, never: never})
		case "include":
			for _, pat := range args {
				pat = expandHome(pat)
				if !filepath.IsAbs(pat) {
					pat = filepath.Join(filepath.Dir(DefaultConfigPath()), pat)
				}
				files, _ := filepath.Glob(pat)
				cur := c.blocks[len(c.blocks)-1]
				for _, inc := range files {
					if err := c.loadIn(inc, depth+1, cur); err != nil {
						return err
					}
				}
				// After the Include the enclosing block continues.
				c.blocks = append(c.blocks, block{patterns: cur.patterns, match: cur.match, conds: cur.conds, never: cur.never})
			}
		default:
			b := &c.blocks[len(c.blocks)-1]
			b.opts = append(b.opts, option{key, strings.Join(args, " ")})
		}
	}
	return sc.Err()
}

// splitLine splits "Key value", "Key=value" and quoted arguments.
func splitLine(s string) (string, []string) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] == '#' {
		return "", nil
	}
	i := strings.IndexAny(s, " \t=")
	if i < 0 {
		return strings.ToLower(s), nil
	}
	key := strings.ToLower(s[:i])
	rest := strings.TrimLeft(s[i:], " \t")
	rest = strings.TrimPrefix(rest, "=")
	var args []string
	var cur strings.Builder
	inQuote, have := false, false
	for _, r := range strings.TrimSpace(rest) {
		switch {
		case r == '"':
			inQuote = !inQuote
			have = true
		case (r == ' ' || r == '\t') && !inQuote:
			if have {
				args = append(args, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		args = append(args, cur.String())
	}
	return key, args
}

// Hosts returns the aliases of the Host blocks that name a concrete host
// (no wildcard, no negation), in file order, without duplicates.
func (c *Config) Hosts() []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range c.blocks {
		for _, p := range b.patterns {
			if strings.ContainsAny(p, "*?!") || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Get returns the first value of key for host (OpenSSH semantics: the
// first obtained value wins; blocks are evaluated in file order).
func (c *Config) Get(host, key string) string {
	if v := c.GetAll(host, key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// GetAll returns all values of key for host in order (IdentityFile may
// be given several times; all are tried).
func (c *Config) GetAll(host, key string) []string {
	key = strings.ToLower(key)
	var out []string
	for _, b := range c.blocks {
		if !b.applies(host) {
			continue
		}
		for _, o := range b.opts {
			if o.key == key {
				out = append(out, o.value)
				if key != "identityfile" {
					return out // first value wins
				}
			}
		}
	}
	return out
}

func (b block) applies(host string) bool {
	if b.match || b.never {
		return false
	}
	if len(b.patterns) > 0 && !matches(b.patterns, host) {
		return false
	}
	for _, c := range b.conds {
		if !matches(c, host) {
			return false
		}
	}
	return true
}

// matches applies a Host pattern list: any positive match and no negated
// match.
func matches(patterns []string, host string) bool {
	ok := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		m, _ := matchGlob(strings.ToLower(p), strings.ToLower(host))
		if m && neg {
			return false
		}
		if m {
			ok = true
		}
	}
	return ok
}

// ExpandPath expands a leading ~ like ssh does.
func ExpandPath(p string) string { return expandHome(p) }

// matchGlob matches an ssh_config pattern (* and ?) against a name.
func matchGlob(pattern, name string) (bool, error) {
	// Host names and algorithm names contain no '/', so filepath.Match
	// behaves like OpenSSH's match_pattern here; '[' is literal there.
	return filepath.Match(strings.ReplaceAll(pattern, "[", "\\["), name)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// Target is everything needed to reach one SSH server.
type Target struct {
	Alias          string // the name used in ~/.ssh/config, if any
	Host           string // HostName
	Port           int
	User           string
	IdentityFiles  []string
	IdentitiesOnly bool
	// ProxyJump hops, outermost first (already resolved).
	Jumps []Target
	// KnownHosts files (UserKnownHostsFile), ~ expanded; new keys are
	// added to the first.
	KnownHosts []string
	// GlobalKnownHosts (GlobalKnownHostsFile), only read; empty: the
	// system files /etc/ssh/ssh_known_hosts{,2}.
	GlobalKnownHosts []string
	// HostKeyAlias is the name host keys are filed under, if set.
	HostKeyAlias          string
	StrictHostKeyChecking string // yes | accept-new | no | ask (default ask)
	HostKeyAlgorithms     string // OpenSSH syntax: list, +list, -list, ^list
	KexAlgorithms         string
	PubkeyAccepted        string
	Ciphers               string
	MACs                  string
	ServerAliveInterval   int // seconds; 0 = default (30)
	ConnectTimeout        int // seconds; 0 = default (15)
}

// Addr is host:port of the target.
func (t Target) Addr() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return joinHostPort(t.Host, port)
}

func joinHostPort(h string, p int) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		return "[" + h + "]:" + strconv.Itoa(p)
	}
	return h + ":" + strconv.Itoa(p)
}

// String is user@host:port (for messages).
func (t Target) String() string {
	s := t.Addr()
	if t.User != "" {
		s = t.User + "@" + s
	}
	if t.Alias != "" && t.Alias != t.Host {
		s = t.Alias + " (" + s + ")"
	}
	return s
}

// Resolve builds the target for name — an alias from the configuration or
// a host name — like ssh does: options of all matching blocks, first value
// wins. ProxyJump hops are resolved the same way.
func (c *Config) Resolve(name string) (Target, error) {
	return c.resolve(name, 0)
}

func (c *Config) resolve(spec string, depth int) (Target, error) {
	if depth > 8 {
		return Target{}, fmt.Errorf("ProxyJump chain too long at %s", spec)
	}
	user, name, port, err := SplitDest(spec)
	if err != nil {
		return Target{}, err
	}
	t := Target{Alias: name, Host: name, User: user, Port: port}
	if c == nil {
		c = &Config{}
	}
	if v := c.Get(name, "hostname"); v != "" {
		t.Host = strings.ReplaceAll(v, "%h", name)
	}
	if t.User == "" {
		t.User = c.Get(name, "user")
	}
	if t.Port == 0 {
		if v := c.Get(name, "port"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return t, fmt.Errorf("ssh config: Port %q for %s", v, name)
			}
			t.Port = n
		}
	}
	for _, f := range c.GetAll(name, "identityfile") {
		if strings.EqualFold(f, "none") {
			continue
		}
		t.IdentityFiles = append(t.IdentityFiles, expandTokens(f, t))
	}
	t.IdentitiesOnly = strings.EqualFold(c.Get(name, "identitiesonly"), "yes")
	for _, f := range strings.Fields(c.Get(name, "userknownhostsfile")) {
		if !strings.EqualFold(f, "none") {
			t.KnownHosts = append(t.KnownHosts, expandTokens(f, t))
		}
	}
	for _, f := range strings.Fields(c.Get(name, "globalknownhostsfile")) {
		if !strings.EqualFold(f, "none") {
			t.GlobalKnownHosts = append(t.GlobalKnownHosts, expandTokens(f, t))
		}
	}
	t.HostKeyAlias = c.Get(name, "hostkeyalias")
	switch v := strings.ToLower(c.Get(name, "stricthostkeychecking")); v {
	case "yes", "true":
		t.StrictHostKeyChecking = "yes"
	case "no", "false", "off":
		t.StrictHostKeyChecking = "no"
	default:
		t.StrictHostKeyChecking = v // accept-new, ask, ""
	}
	t.HostKeyAlgorithms = c.Get(name, "hostkeyalgorithms")
	t.KexAlgorithms = c.Get(name, "kexalgorithms")
	t.PubkeyAccepted = c.Get(name, "pubkeyacceptedalgorithms")
	if t.PubkeyAccepted == "" {
		t.PubkeyAccepted = c.Get(name, "pubkeyacceptedkeytypes") // old name
	}
	t.Ciphers = c.Get(name, "ciphers")
	t.MACs = c.Get(name, "macs")
	t.ServerAliveInterval, _ = strconv.Atoi(c.Get(name, "serveraliveinterval"))
	t.ConnectTimeout, _ = strconv.Atoi(c.Get(name, "connecttimeout"))
	if pc := c.Get(name, "proxycommand"); pc != "" && !strings.EqualFold(pc, "none") {
		return t, fmt.Errorf("ssh config: %s uses ProxyCommand, which dbcc does not support (use ProxyJump)", name)
	}
	if pj := c.Get(name, "proxyjump"); pj != "" && !strings.EqualFold(pj, "none") {
		for _, hop := range strings.Split(pj, ",") {
			jt, err := c.resolve(strings.TrimSpace(hop), depth+1)
			if err != nil {
				return t, err
			}
			// A hop's own jumps come before it.
			t.Jumps = append(t.Jumps, jt.Jumps...)
			jt.Jumps = nil
			t.Jumps = append(t.Jumps, jt)
		}
	}
	return t, nil
}

// SplitDest parses [user@]host[:port] (also ssh://user@host:port).
func SplitDest(s string) (user, host string, port int, err error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "ssh://")
	if i := strings.LastIndex(s, "@"); i >= 0 {
		user, s = s[:i], s[i+1:]
	}
	host = s
	if strings.HasPrefix(s, "[") {
		if j := strings.Index(s, "]"); j > 0 {
			host = s[1:j]
			if rest := s[j+1:]; strings.HasPrefix(rest, ":") {
				port, err = strconv.Atoi(rest[1:])
			}
		}
	} else if strings.Count(s, ":") == 1 {
		h, p, _ := strings.Cut(s, ":")
		host = h
		port, err = strconv.Atoi(p)
	}
	if err != nil || host == "" {
		return "", "", 0, fmt.Errorf("invalid SSH destination %q (use [user@]host[:port] or a Host from ~/.ssh/config)", s)
	}
	return user, host, port, nil
}

// expandTokens expands ~ and the tokens %d %h %u %r %p %% of OpenSSH
// file names (the common ones).
func expandTokens(s string, t Target) string {
	home, _ := os.UserHomeDir()
	local := os.Getenv("USER")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'd':
			b.WriteString(home)
		case 'h':
			b.WriteString(t.Host)
		case 'u':
			b.WriteString(local)
		case 'r':
			b.WriteString(t.User)
		case 'p':
			b.WriteString(strconv.Itoa(max(t.Port, 22)))
		case 'n':
			b.WriteString(t.Alias)
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(s[i])
		}
	}
	return expandHome(b.String())
}
