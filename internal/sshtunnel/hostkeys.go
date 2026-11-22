package sshtunnel

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type hostKeyError struct{ msg string }

func (e *hostKeyError) Error() string { return e.msg }
func (e *hostKeyError) Unwrap() error { return ErrUnknownHostKey }

func defaultKnownHosts() []string {
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".ssh", "known_hosts"), filepath.Join(home, ".ssh", "known_hosts2")}
}

// GlobalKnownHosts are the system-wide known_hosts files (OpenSSH's
// GlobalKnownHostsFile default); read for checking, never written. A
// variable for tests.
var GlobalKnownHosts = []string{"/etc/ssh/ssh_known_hosts", "/etc/ssh/ssh_known_hosts2"}

// knownHostsAddr is host:port under which a host's keys are filed: the
// HostKeyAlias if set, else the host. knownhosts.Normalize of it is the
// name in the file ([host]:port unless the port is 22).
func knownHostsAddr(h Target) string {
	port := h.Port
	if port == 0 {
		port = 22
	}
	host := h.Host
	if h.HostKeyAlias != "" {
		host = h.HostKeyAlias
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// hostKeyCallback checks keys against the user's and the global
// known_hosts files, as OpenSSH does. An unknown key is confirmed by the
// user (StrictHostKeyChecking ask, the default), added silently
// (accept-new, no) or rejected (yes); a changed key is always rejected.
// Accepted keys are appended to the first user known_hosts file.
func (tu *Tunnel) hostKeyCallback(hs *handshake) (ssh.HostKeyCallback, []string, error) {
	h := hs.hop
	user := h.KnownHosts
	if len(user) == 0 {
		user = defaultKnownHosts()
	}
	global := h.GlobalKnownHosts
	if len(global) == 0 {
		global = GlobalKnownHosts
	}
	exist := func(files []string) []string {
		var out []string
		for _, f := range files {
			if st, err := os.Stat(f); err == nil && !st.IsDir() {
				out = append(out, f)
			}
		}
		return out
	}
	var checks []ssh.HostKeyCallback
	if f := exist(user); len(f) > 0 {
		cb, err := knownhosts.New(f...)
		if err != nil {
			return nil, nil, fmt.Errorf("known_hosts: %w", err)
		}
		checks = append(checks, cb)
	}
	// System files are the admin's: one that cannot be read or parsed is
	// skipped (as OpenSSH skips bad lines) rather than stopping every
	// tunnel. Each is loaded on its own so one bad file spoils no other.
	for _, f := range exist(global) {
		if cb, err := knownhosts.New(f); err == nil {
			checks = append(checks, cb)
		}
	}
	check := combineChecks(checks)
	addr := knownHostsAddr(h)
	name := knownhosts.Normalize(addr)
	knownAlgos := knownKeyAlgos(check, addr)
	writeTo := user[0]
	return func(_ string, remote net.Addr, key ssh.PublicKey) error {
		if check != nil {
			err := check(addr, remote, key)
			if err == nil {
				return nil
			}
			var ke *knownhosts.KeyError
			if !errors.As(err, &ke) {
				return err // e.g. a revoked key
			}
			if len(ke.Want) > 0 {
				w := ke.Want[0]
				return &hostKeyError{fmt.Sprintf(
					"HOST KEY CHANGED for %s: the server sent %s %s, but %s:%d has %s %s. "+
						"This may be an attack (or the server was reinstalled). Connection refused; "+
						"remove the old entry (ssh-keygen -R %s) only if you know why the key changed",
					name, key.Type(), ssh.FingerprintSHA256(key), w.Filename, w.Line,
					w.Key.Type(), ssh.FingerprintSHA256(w.Key), name)}
			}
		}
		fp := ssh.FingerprintSHA256(key)
		switch h.StrictHostKeyChecking {
		case "yes":
			return &hostKeyError{fmt.Sprintf("unknown host key for %s (%s %s) and StrictHostKeyChecking is yes; connect once with ssh to add it", name, key.Type(), fp)}
		case "accept-new", "no":
		default:
			if tu.cb.ConfirmHostKey == nil {
				return &hostKeyError{fmt.Sprintf("unknown host key for %s (%s %s); connect once with ssh (or in the dbcc UI) to accept it", name, key.Type(), fp)}
			}
			var ok bool
			var err error
			hs.userWait(func() {
				ok, err = tu.cb.ConfirmHostKey(fmt.Sprintf(
					"The authenticity of host %s can't be established.\n%s key fingerprint:\n%s\n\nTrust this host and add it to %s?",
					name, key.Type(), fp, writeTo))
			})
			if err != nil {
				return err
			}
			if !ok {
				return &hostKeyError{fmt.Sprintf("host key for %s not accepted", name)}
			}
		}
		return appendKnownHost(writeTo, name, key)
	}, knownAlgos, nil
}

// combineChecks checks a key against several known_hosts databases, as
// OpenSSH does: known in any → ok; otherwise a KeyError listing the keys
// they have for the host (none: unknown host); other errors (a revoked
// key) win.
func combineChecks(checks []ssh.HostKeyCallback) ssh.HostKeyCallback {
	if len(checks) == 0 {
		return nil
	}
	return func(addr string, remote net.Addr, key ssh.PublicKey) error {
		all := &knownhosts.KeyError{}
		known := false
		for _, c := range checks {
			err := c(addr, remote, key)
			var ke *knownhosts.KeyError
			switch {
			case err == nil:
				known = true
			case errors.As(err, &ke):
				all.Want = append(all.Want, ke.Want...)
			default:
				return err
			}
		}
		if known {
			return nil
		}
		return all
	}
}

// knownKeyAlgos returns the host key algorithms known_hosts has for addr
// (by asking the checker with a key it cannot know).
func knownKeyAlgos(check ssh.HostKeyCallback, addr string) []string {
	if check == nil {
		return nil
	}
	probe, err := ssh.NewPublicKey(probeKey)
	if err != nil {
		return nil
	}
	err = check(addr, &net.TCPAddr{}, probe)
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) {
		return nil
	}
	var algos []string
	for _, w := range ke.Want {
		for _, a := range algosForKeyType(w.Key.Type()) {
			if !slices.Contains(algos, a) {
				algos = append(algos, a)
			}
		}
	}
	return algos
}

func algosForKeyType(t string) []string {
	if t == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{t}
}

func appendKnownHost(path, name string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("add host key to %s: %w", path, err)
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, knownhosts.Line([]string{name}, key))
	return err
}

// probeKey is a key no known_hosts file contains (see knownKeyAlgos).
var probeKey = ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public()
