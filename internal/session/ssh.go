package session

import (
	"context"
	"fmt"
	"strings"

	"dbcc/internal/config"
	"dbcc/internal/sshtunnel"
)

// Hooks connect the opening of a profile to the user: password and
// passphrase prompts and the host key question of an SSH tunnel (A27).
type Hooks struct {
	Prompt config.Prompter
	// ConfirmHostKey asks whether to trust an unknown SSH host key. Nil:
	// unknown host keys are rejected (headless).
	ConfirmHostKey func(msg string) (bool, error)
	// NoStore: do not store the SSH secret even with ssh.password =
	// "stored" (the connection editor's test stores only on Save).
	NoStore bool
	// SecretUsed, if set, receives the SSH password or passphrase that was
	// entered for the target (not for a jump host, never a one-time code).
	SecretUsed func(kind, secret string)
}

// SSHConfigPath is the OpenSSH client configuration read for tunnels; a
// variable for tests.
var SSHConfigPath = sshtunnel.DefaultConfigPath

// SSHTarget builds the SSH target of a profile. With ssh.alias, the Host
// of ~/.ssh/config is resolved and the fields saved in the profile take
// precedence. Without an alias, ssh.host is looked up like a name given to
// ssh ([user@]host[:port], or a Host of the configuration).
func SSHTarget(p *config.Profile) (sshtunnel.Target, error) {
	s := p.SSH
	if s == nil {
		return sshtunnel.Target{}, fmt.Errorf("profile %q has no SSH tunnel", p.Name)
	}
	cfg, err := sshtunnel.LoadConfig(SSHConfigPath())
	if err != nil {
		return sshtunnel.Target{}, fmt.Errorf("ssh config: %w", err)
	}
	name := s.Alias
	if name == "" {
		name = s.Host
	}
	t, err := cfg.Resolve(name)
	if err != nil {
		return t, err
	}
	if s.Alias != "" && s.Host != "" {
		t.Host = s.Host // the HostName taken over when the connection was saved
	}
	if s.Port != 0 {
		t.Port = s.Port
	}
	if s.User != "" {
		t.User = s.User
	}
	if s.IdentityFile != "" {
		f := sshtunnel.ExpandPath(s.IdentityFile)
		files := []string{f}
		for _, x := range t.IdentityFiles {
			if x != f {
				files = append(files, x)
			}
		}
		t.IdentityFiles = files
	}
	if s.ProxyJump != "" {
		t.Jumps = nil
		for _, hop := range strings.Split(s.ProxyJump, ",") {
			jt, err := cfg.Resolve(strings.TrimSpace(hop))
			if err != nil {
				return t, err
			}
			t.Jumps = append(t.Jumps, jt.Jumps...)
			jt.Jumps = nil
			t.Jumps = append(t.Jumps, jt)
		}
	}
	return t, nil
}

// secretKind is how a secret is filed in the credential store: what it
// answers and for which server (host:port), so a secret stored for one
// server is never offered to another (e.g. after the SSH host of the
// profile was changed).
func secretKind(kind, key, hop string) string {
	if kind == "passphrase" {
		kind = "passphrase:" + key
	}
	return kind + "@" + hop
}

// OpenTunnel opens the SSH tunnel of a profile. With ssh.password =
// "stored" a stored secret answers only the question it was stored for
// (the target's password, or the passphrase of that key file), once; the
// secret that was entered is stored after the connection worked.
func OpenTunnel(ctx context.Context, p *config.Profile, h Hooks) (*sshtunnel.Tunnel, error) {
	t, err := SSHTarget(p)
	if err != nil {
		return nil, err
	}
	stored := p.SSH.Password == "stored"
	var storedKind, storedSecret string
	haveStored := false
	if stored {
		if storedKind, storedSecret, haveStored, err = config.StoredSSHSecret(p.Name); err != nil {
			return nil, err
		}
	}
	cb := sshtunnel.Callbacks{ConfirmHostKey: h.ConfirmHostKey}
	cb.Secret = func(r sshtunnel.SecretRequest) (string, error) {
		if haveStored && r.Target && r.Kind != "answer" && secretKind(r.Kind, r.Key, r.Hop) == storedKind {
			haveStored = false // offered once; a wrong one is asked for
			return storedSecret, nil
		}
		if h.Prompt == nil {
			return "", fmt.Errorf("%s needs a %s but no prompt is available", t.String(), r.Kind)
		}
		return h.Prompt(r.Prompt)
	}
	tu, err := sshtunnel.Open(ctx, t, cb)
	if err != nil {
		return nil, err
	}
	if used := tu.UsedSecret(); used != nil {
		kind := secretKind(used.Kind, used.Key, t.Addr())
		if h.SecretUsed != nil {
			h.SecretUsed(kind, used.Value)
		}
		if stored && !h.NoStore && (kind != storedKind || used.Value != storedSecret) {
			if err := config.StoreSSHSecret(p.Name, kind, used.Value); err != nil {
				tu.Close()
				return nil, fmt.Errorf("store SSH secret for %s: %w", p.Name, err)
			}
		}
	}
	return tu, nil
}

// OpenWith connects a profile like Open, through its SSH tunnel if it has
// one; the tunnel is closed with the session.
func OpenWith(ctx context.Context, p *config.Profile, cred config.Credentials, database string, h Hooks) (*Session, error) {
	if p.SSH == nil {
		return Open(ctx, p, cred, database)
	}
	tu, err := OpenTunnel(ctx, p, h)
	if err != nil {
		return nil, err
	}
	cred.Dial = tu.DialContext
	if cred.Host == "" {
		cred.Host = "127.0.0.1"
	}
	s, err := Open(ctx, p, cred, database)
	if err != nil {
		tu.Close()
		return nil, fmt.Errorf("%w (through SSH %s)", err, tu.Target().String())
	}
	s.mu.Lock()
	s.tunnel = tu
	s.mu.Unlock()
	return s, nil
}
