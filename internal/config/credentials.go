package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Prompter asks the user for a secret.
type Prompter func(prompt string) (string, error)

// ResolveCredentials resolves host/port/socket/user/password for a profile.
func ResolveCredentials(p *Profile, prompt Prompter) (Credentials, error) {
	c := Credentials{Host: p.Host, Port: p.Port, Socket: p.Socket, User: p.User}
	src := p.Password
	switch {
	case src == "" || src == "prompt":
		if prompt == nil {
			return c, fmt.Errorf("profile %q: password prompt required but no prompter available", p.Name)
		}
		pw, err := prompt("Password for " + p.Name + ": ")
		if err != nil {
			return c, err
		}
		c.Password = pw
	case src == "stored":
		pw, ok, err := StoredPassword(p.Name)
		if err != nil {
			return c, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		if !ok {
			// Nothing stored yet: ask, and let the caller offer to store it.
			if prompt == nil {
				return c, fmt.Errorf("profile %q: no stored password (credential store %s)", p.Name, CredentialsPath())
			}
			if pw, err = prompt("Password for " + p.Name + ": "); err != nil {
				return c, err
			}
			c.PromptedForStore = true
		}
		c.Password = pw
	case strings.HasPrefix(src, "env:"):
		name := strings.TrimPrefix(src, "env:")
		v, ok := os.LookupEnv(name)
		if !ok {
			return c, fmt.Errorf("profile %q: environment variable %s is not set", p.Name, name)
		}
		c.Password = v
	case src == "mycnf":
		m, err := ReadMyCnf(filepath.Join(HomeDir(), ".my.cnf"))
		if err != nil {
			return c, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		if c.User == "" {
			c.User = m["user"]
		}
		if c.Host == "" {
			c.Host = m["host"]
		}
		if c.Socket == "" {
			c.Socket = m["socket"]
		}
		if c.Port == 0 && c.Socket == "" {
			if n, err := strconv.Atoi(m["port"]); err == nil {
				c.Port = n
			}
		}
		c.Password = m["password"]
	case src == "pgpass":
		path := os.Getenv("PGPASSFILE")
		if path == "" {
			path = filepath.Join(HomeDir(), ".pgpass")
		}
		host := c.Host
		if host == "" {
			host = "localhost"
		}
		pw, ok, err := LookupPgpass(path, host, c.Port, p.Database, c.User)
		if err != nil {
			return c, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		if !ok {
			return c, fmt.Errorf("profile %q: no matching entry in %s", p.Name, path)
		}
		c.Password = pw
	default:
		return c, fmt.Errorf("profile %q: passwords must not be stored in the profile file", p.Name)
	}
	return c, nil
}

// ReadMyCnf reads the [client] section of a my.cnf style file.
func ReadMyCnf(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	inClient := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' || line[0] == '!' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				continue
			}
			inClient = strings.EqualFold(strings.TrimSpace(line[1:end]), "client")
			continue
		}
		if !inClient {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		out[k] = parseMyCnfValue(strings.TrimSpace(v))
	}
	return out, sc.Err()
}

func parseMyCnfValue(v string) string {
	if v == "" {
		return ""
	}
	if q := v[0]; q == '"' || q == '\'' {
		var b strings.Builder
		for i := 1; i < len(v); i++ {
			ch := v[i]
			if ch == '\\' && i+1 < len(v) {
				i++
				switch v[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				case 's':
					b.WriteByte(' ')
				default:
					b.WriteByte(v[i])
				}
				continue
			}
			if ch == q {
				break
			}
			b.WriteByte(ch)
		}
		return b.String()
	}
	// unquoted: strip trailing comment introduced by whitespace + # (or leading #)
	for i := 0; i < len(v); i++ {
		if v[i] == '#' && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i]
			break
		}
	}
	return strings.TrimSpace(v)
}

// LookupPgpass finds the password for the first matching pgpass entry.
func LookupPgpass(path, host string, port int, db, user string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer f.Close()
	want := []string{host, strconv.Itoa(port), db, user}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r\n")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := splitPgpass(line)
		if len(fields) != 5 {
			continue
		}
		match := true
		for i := 0; i < 4; i++ {
			if fields[i] != "*" && fields[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return fields[4], true, nil
		}
	}
	return "", false, sc.Err()
}

// splitPgpass splits on unescaped colons and unescapes \: and \\.
func splitPgpass(line string) []string {
	var fields []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case ch == '\\' && i+1 < len(line):
			i++
			b.WriteByte(line[i])
		case ch == ':':
			fields = append(fields, b.String())
			b.Reset()
		default:
			b.WriteByte(ch)
		}
	}
	return append(fields, b.String())
}
