package sshtunnel

// Tests for the findings of the security review of the SSH tunnel (A27).

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"dbcc/internal/sshtunnel/sshtest"
)

func pw(s string) Callbacks {
	return Callbacks{Secret: func(SecretRequest) (string, error) { return s, nil }}
}

// The system known_hosts file counts: a key that differs from it is
// refused as changed (not offered as "unknown"), also with accept-new.
func TestGlobalKnownHostsAreChecked(t *testing.T) {
	home := isolate(t)
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	global := filepath.Join(home, "ssh_known_hosts")
	old := GlobalKnownHosts
	GlobalKnownHosts = []string{global}
	defer func() { GlobalKnownHosts = old }()
	addr := knownhosts.Normalize(srv.Addr)

	other := sshtest.NewSigner(t) // the "real" key, distributed by the admin
	os.WriteFile(global, []byte(knownhosts.Line([]string{addr}, other.PublicKey())+"\n"), 0o644)
	tg := targetFor(srv)
	tg.StrictHostKeyChecking = "accept-new"
	_, err := Open(context.Background(), tg, pw("pw"))
	if err == nil || !strings.Contains(err.Error(), "HOST KEY CHANGED") {
		t.Fatalf("key differing from the global file: %v", err)
	}
	if kh, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts")); len(kh) > 0 {
		t.Fatalf("accept-new wrote the mismatching key: %q", kh)
	}
	// With the right key in the global file: connects, asks nothing,
	// writes nothing (the global file is never written).
	os.WriteFile(global, []byte(knownhosts.Line([]string{addr}, srv.HostKey.PublicKey())+"\n"), 0o644)
	tu, err := Open(context.Background(), targetFor(srv), Callbacks{
		Secret:         func(SecretRequest) (string, error) { return "pw", nil },
		ConfirmHostKey: func(string) (bool, error) { t.Error("asked"); return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	tu.Close()
}

// HostKeyAlias: keys are looked up and filed under the alias.
func TestHostKeyAlias(t *testing.T) {
	home := isolate(t)
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	tg := targetFor(srv)
	tg.HostKeyAlias = "db-bastion"
	tg.StrictHostKeyChecking = "accept-new"
	tu, err := Open(context.Background(), tg, pw("pw"))
	if err != nil {
		t.Fatal(err)
	}
	tu.Close()
	kh, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	_, port, _ := net.SplitHostPort(srv.Addr)
	if !strings.HasPrefix(string(kh), "[db-bastion]:"+port+" ") {
		t.Fatalf("known_hosts: %q", kh)
	}
	// The address itself is unknown under its own name.
	tg2 := targetFor(srv)
	if _, err := Open(context.Background(), tg2, pw("pw")); !errors.Is(err, ErrUnknownHostKey) {
		t.Errorf("without the alias: %v", err)
	}
}

// Secret requests say who asks: hop, kind and whether it is the target;
// UsedSecret is the target's only.
func TestSecretRequestsIdentifyTheQuestion(t *testing.T) {
	isolate(t)
	jump := sshtest.Start(t, sshtest.Options{Password: "j"})
	target := sshtest.Start(t, sshtest.Options{Password: "t"})
	tg := targetFor(target)
	tg.StrictHostKeyChecking = "accept-new"
	jt := targetFor(jump)
	jt.StrictHostKeyChecking = "accept-new"
	tg.Jumps = []Target{jt}
	var reqs []SecretRequest
	tu, err := Open(context.Background(), tg, Callbacks{Secret: func(r SecretRequest) (string, error) {
		reqs = append(reqs, r)
		if r.Hop == jump.Addr {
			return "j", nil
		}
		return "t", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer tu.Close()
	if len(reqs) != 2 || reqs[0].Target || reqs[0].Hop != jump.Addr || !reqs[1].Target || reqs[1].Hop != target.Addr ||
		reqs[0].Kind != "password" || reqs[1].Kind != "password" {
		t.Errorf("requests: %+v", reqs)
	}
	if u := tu.UsedSecret(); u == nil || u.Value != "t" {
		t.Errorf("used: %+v", u)
	}
}

// A wrong password is asked for again (keyboard-interactive after the
// password method), not silently reused.
func TestWrongPasswordIsAskedAgain(t *testing.T) {
	isolate(t)
	srv := sshtest.Start(t, sshtest.Options{Password: "right", KbdPassword: true})
	tg := targetFor(srv)
	tg.StrictHostKeyChecking = "accept-new"
	answers := []string{"wrong", "right"}
	n := 0
	tu, err := Open(context.Background(), tg, Callbacks{Secret: func(r SecretRequest) (string, error) {
		a := answers[min(n, 1)]
		n++
		return a, nil
	}})
	if err != nil {
		t.Fatalf("after a wrong password: %v (asked %d times)", err, n)
	}
	tu.Close()
	if n != 2 || tu.UsedSecret().Value != "right" {
		t.Errorf("asked %d times, used %+v", n, tu.UsedSecret())
	}
}

// Several dials after the SSH connection was lost rebuild it once.
func TestConcurrentReconnect(t *testing.T) {
	isolate(t)
	echo := sshtest.EchoServer(t)
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	tg := targetFor(srv)
	tg.StrictHostKeyChecking = "accept-new"
	var prompts sync.Map
	count := 0
	var mu sync.Mutex
	tu, err := Open(context.Background(), tg, Callbacks{Secret: func(r SecretRequest) (string, error) {
		mu.Lock()
		count++
		mu.Unlock()
		prompts.Store(count, r.Prompt)
		return "pw", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer tu.Close()
	srv.DropAll()
	time.Sleep(100 * time.Millisecond)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := tu.DialContext(context.Background(), "tcp", echo)
			if err != nil {
				errs <- err
				return
			}
			c.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if srv.Logins.Load() != 2 || count != 1 {
		t.Errorf("logins %d (want 2), prompts %d (want 1)", srv.Logins.Load(), count)
	}
}

// A target behind a jump host that never answers the SSH handshake
// times out (ConnectTimeout), and ctx cancels it.
func TestHandshakeTimeoutThroughJump(t *testing.T) {
	isolate(t)
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			defer c.Close() // hold it, never speak
		}
	}()
	jump := sshtest.Start(t, sshtest.Options{Password: "j"})
	jt := targetFor(jump)
	jt.StrictHostKeyChecking = "accept-new"
	h, p, _ := net.SplitHostPort(silent.Addr().String())
	port, _ := strconv.Atoi(p)
	tg := Target{Host: h, Port: port, User: "u", ConnectTimeout: 1, Jumps: []Target{jt}}
	start := time.Now()
	_, err = Open(context.Background(), tg, pw("j"))
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout: %v after %s", err, time.Since(start))
	}
	tg.ConnectTimeout = 60
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, err = Open(ctx, tg, pw("j"))
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("cancel: %v after %s", err, time.Since(start))
	}
}

// Tunnel connections support deadlines: a read past its deadline fails
// with a timeout; an idle connection with an expired deadline stays open.
func TestTunnelConnDeadlines(t *testing.T) {
	isolate(t)
	silent, _ := net.Listen("tcp", "127.0.0.1:0")
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 64)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if string(buf[:n]) == "ping" {
						c.Write([]byte("pong"))
					}
				}
			}()
		}
	}()
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	tg := targetFor(srv)
	tg.StrictHostKeyChecking = "accept-new"
	tu, err := Open(context.Background(), tg, pw("pw"))
	if err != nil {
		t.Fatal(err)
	}
	defer tu.Close()
	c, err := tu.DialContext(context.Background(), "tcp", silent.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.RemoteAddr().String() != silent.Addr().String() {
		t.Errorf("remote addr %s", c.RemoteAddr())
	}
	// Idle with an expired deadline, then cleared: still usable.
	if err := c.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	c.SetDeadline(time.Time{})
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := c.Read(buf); err != nil || string(buf) != "pong" {
		t.Fatalf("after an idle expired deadline: %q %v", buf, err)
	}
	// A read with nothing to read ends at its deadline as a timeout.
	c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	start := time.Now()
	_, err = c.Read(buf)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() || time.Since(start) > 3*time.Second {
		t.Fatalf("read past deadline: %v after %s", err, time.Since(start))
	}
	// An operation after an expired deadline fails at once.
	c2, _ := tu.DialContext(context.Background(), "tcp", silent.Addr().String())
	defer c2.Close()
	c2.SetWriteDeadline(time.Now().Add(-time.Second))
	if _, err := c2.Write([]byte("x")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("write after deadline: %v", err)
	}
}

// RSA keys sign with SHA-2 only unless +ssh-rsa is configured; a list
// without RSA leaves RSA keys out.
func TestRSAAlgorithms(t *testing.T) {
	signer := rsaSigner(t)
	if s, ok := withAlgos(signer, nil); !ok || strings.Join(s.(ssh.MultiAlgorithmSigner).Algorithms(), ",") != "rsa-sha2-512,rsa-sha2-256" {
		t.Errorf("default: %v", s)
	}
	if s, ok := withAlgos(signer, pubkeyAlgos("+ssh-rsa")); !ok || !strings.Contains(strings.Join(s.(ssh.MultiAlgorithmSigner).Algorithms(), ","), "ssh-rsa") {
		t.Errorf("+ssh-rsa: %v", s)
	}
	if _, ok := withAlgos(signer, pubkeyAlgos("ssh-ed25519")); ok {
		t.Error("RSA key offered although PubkeyAcceptedAlgorithms excludes RSA")
	}
}

func TestEmptyAlgorithmListIsAnError(t *testing.T) {
	isolate(t)
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	tg := targetFor(srv)
	tg.KexAlgorithms = "no-such-kex"
	if _, err := Open(context.Background(), tg, pw("pw")); err == nil || !strings.Contains(err.Error(), "names no algorithm") {
		t.Errorf("unknown KexAlgorithms: %v", err)
	}
}

func rsaSigner(t *testing.T) ssh.Signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A broken system known_hosts file is skipped, not fatal.
func TestBrokenGlobalKnownHostsIgnored(t *testing.T) {
	home := isolate(t)
	bad := filepath.Join(home, "broken_known_hosts")
	os.WriteFile(bad, []byte("this is not a known_hosts line\n@@@ garbage\n"), 0o644)
	old := GlobalKnownHosts
	GlobalKnownHosts = []string{bad}
	defer func() { GlobalKnownHosts = old }()
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	tg := targetFor(srv)
	tg.StrictHostKeyChecking = "accept-new"
	tu, err := Open(context.Background(), tg, pw("pw"))
	if err != nil {
		t.Fatalf("broken global file: %v", err)
	}
	tu.Close()
}

// A dial waiting for another login gives up when its ctx ends.
func TestConnectWaitHonoursContext(t *testing.T) {
	isolate(t)
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	tg := targetFor(srv)
	tg.StrictHostKeyChecking = "accept-new"
	tu, err := Open(context.Background(), tg, pw("pw"))
	if err != nil {
		t.Fatal(err)
	}
	defer tu.Close()
	tu.connSem <- struct{}{} // another login is "running"
	defer func() { <-tu.connSem }()
	srv.DropAll()
	tu.mu.Lock()
	tu.closeClientsLocked()
	tu.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := tu.DialContext(ctx, "tcp", "127.0.0.1:1"); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("wait: %v after %s", err, time.Since(start))
	}
}
