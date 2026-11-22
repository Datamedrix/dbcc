package sshtunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"dbcc/internal/sshtunnel/sshtest"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("USER", "tester")
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	return home
}

func targetFor(s *sshtest.Server) Target {
	h, p, _ := net.SplitHostPort(s.Addr)
	port, _ := strconv.Atoi(p)
	return Target{Host: h, Port: port, User: "u"}
}

func roundTrip(t *testing.T, tu *Tunnel, addr string) {
	t.Helper()
	c, err := tu.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("ping"))
	buf := make([]byte, 64)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "echo: ping" {
		t.Fatalf("round trip: %q %v", buf[:n], err)
	}
}

func TestHostKeyUnknownConfirmedChanged(t *testing.T) {
	home := isolate(t)
	echo := sshtest.EchoServer(t)
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	secret := func(kind, prompt string) (string, error) { return "pw", nil }

	// Headless (no confirmation): an unknown host key is refused.
	_, err := Open(context.Background(), targetFor(srv), Callbacks{Secret: adapt(secret)})
	if !errors.Is(err, ErrUnknownHostKey) || !strings.Contains(err.Error(), "SHA256:") {
		t.Fatalf("unknown key without confirmation: %v", err)
	}
	// Declined.
	asked := 0
	_, err = Open(context.Background(), targetFor(srv), Callbacks{Secret: adapt(secret),
		ConfirmHostKey: func(msg string) (bool, error) { asked++; return false, nil }})
	if !errors.Is(err, ErrUnknownHostKey) || asked != 1 {
		t.Fatalf("declined: %v (asked %d)", err, asked)
	}
	// Accepted: written to ~/.ssh/known_hosts …
	var question string
	tu, err := Open(context.Background(), targetFor(srv), Callbacks{Secret: adapt(secret),
		ConfirmHostKey: func(msg string) (bool, error) { question = msg; return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(question, ssh.FingerprintSHA256(srv.HostKey.PublicKey())) {
		t.Errorf("question lacks the fingerprint: %s", question)
	}
	roundTrip(t, tu, echo)
	tu.Close()
	kh, _ := os.ReadFile(filepath.Join(home, ".ssh", "known_hosts"))
	if !strings.Contains(string(kh), "ssh-ed25519") {
		t.Fatalf("known_hosts: %q", kh)
	}
	// … and known the next time, without a question.
	tu, err = Open(context.Background(), targetFor(srv), Callbacks{Secret: adapt(secret)})
	if err != nil {
		t.Fatalf("known host: %v", err)
	}
	tu.Close()

	// The same address with another host key: refused, never asked.
	srv.Ln.Close()
	other := startServerAt(t, srv.Addr, sshtest.Options{Password: "pw"})
	_, err = Open(context.Background(), targetFor(other), Callbacks{Secret: adapt(secret),
		ConfirmHostKey: func(string) (bool, error) { t.Error("asked about a changed key"); return true, nil }})
	if err == nil || !strings.Contains(err.Error(), "HOST KEY CHANGED") {
		t.Fatalf("changed key: %v", err)
	}
}

// startServerAt starts a server on a given address (retrying while the old
// listener's port is released).
func startServerAt(t *testing.T, addr string, o sshtest.Options) *sshtest.Server {
	t.Helper()
	var ln net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Skipf("cannot reuse %s: %v", addr, err)
	}
	ln.Close()
	s := startServerOn(t, addr, o)
	return s
}

func startServerOn(t *testing.T, addr string, o sshtest.Options) *sshtest.Server {
	// Same as startServer but on addr.
	o2 := o
	if o2.HostKey == nil {
		o2.HostKey = sshtest.NewSigner(t)
	}
	srv := sshtest.Start(t, o2)
	// Re-listen on the wanted address and forward accepted conns.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				b, err := net.Dial("tcp", srv.Addr)
				if err != nil {
					c.Close()
					return
				}
				go func() { io.Copy(b, c); b.Close() }()
				io.Copy(c, b)
				c.Close()
			}()
		}
	}()
	return &sshtest.Server{Addr: addr, HostKey: o2.HostKey}
}

func TestPasswordAndAcceptNew(t *testing.T) {
	isolate(t)
	echo := sshtest.EchoServer(t)
	srv := sshtest.Start(t, sshtest.Options{Password: "s3cret"})
	tg := targetFor(srv)
	tg.StrictHostKeyChecking = "accept-new"
	prompts := 0
	tu, err := Open(context.Background(), tg, Callbacks{Secret: adapt(func(kind, prompt string) (string, error) {
		prompts++
		if kind != "password" || !strings.Contains(prompt, "u@127.0.0.1") {
			t.Errorf("prompt %s %q", kind, prompt)
		}
		return "s3cret", nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer tu.Close()
	if prompts != 1 || tu.UsedSecret() == nil || tu.UsedSecret().Value != "s3cret" || tu.UsedSecret().Kind != "password" {
		t.Errorf("prompts %d, used %+v", prompts, tu.UsedSecret())
	}
	roundTrip(t, tu, echo)

	// A dropped SSH connection is rebuilt on the next dial, without
	// asking again (the secret is kept in memory).
	srv.DropAll()
	time.Sleep(100 * time.Millisecond)
	roundTrip(t, tu, echo)
	if prompts != 1 || srv.Logins.Load() != 2 {
		t.Errorf("after reconnect: prompts %d, logins %d", prompts, srv.Logins.Load())
	}

	// Wrong Password: a clear error.
	tg2 := tg
	_, err = Open(context.Background(), tg2, Callbacks{Secret: adapt(func(string, string) (string, error) { return "nope", nil })})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("wrong Password: %v", err)
	}
}

func TestEncryptedKeyAsksPassphraseOnlyWhenAccepted(t *testing.T) {
	home := isolate(t)
	echo := sshtest.EchoServer(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "test key", []byte("open sesame"))
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := ssh.NewPublicKey(priv.Public())
	keyFile := filepath.Join(home, ".ssh", "deploy")
	os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600)
	os.WriteFile(keyFile+".pub", ssh.MarshalAuthorizedKey(pub), 0o644)
	// An unencrypted decoy key the server does not accept comes first.
	_, decoy, _ := ed25519.GenerateKey(rand.Reader)
	db, _ := ssh.MarshalPrivateKey(decoy, "")
	decoyFile := filepath.Join(home, ".ssh", "decoy")
	os.WriteFile(decoyFile, pem.EncodeToMemory(db), 0o600)

	srv := sshtest.Start(t, sshtest.Options{Key: pub})
	tg := targetFor(srv)
	tg.StrictHostKeyChecking = "accept-new"
	tg.IdentityFiles = []string{decoyFile, keyFile}
	var asked []string
	tu, err := Open(context.Background(), tg, Callbacks{Secret: adapt(func(kind, prompt string) (string, error) {
		asked = append(asked, kind+": "+prompt)
		if len(asked) == 1 {
			return "wrong", nil
		}
		return "open sesame", nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer tu.Close()
	roundTrip(t, tu, echo)
	if len(asked) != 2 || !strings.HasPrefix(asked[0], "passphrase: Passphrase for "+keyFile) {
		t.Errorf("asked: %q", asked)
	}
	if u := tu.UsedSecret(); u == nil || u.Value != "open sesame" || u.Kind != "passphrase" || u.Key != keyFile {
		t.Errorf("used %+v", tu.UsedSecret())
	}

	// A server that does not accept the key never triggers the question.
	other := sshtest.Start(t, sshtest.Options{Password: "pw"})
	tg3 := targetFor(other)
	tg3.StrictHostKeyChecking = "accept-new"
	tg3.IdentityFiles = []string{keyFile}
	var kinds []string
	tu3, err := Open(context.Background(), tg3, Callbacks{Secret: adapt(func(kind, _ string) (string, error) {
		kinds = append(kinds, kind)
		return "pw", nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	tu3.Close()
	if strings.Join(kinds, ",") != "password" {
		t.Errorf("questions: %v", kinds)
	}
}

func TestProxyJump(t *testing.T) {
	isolate(t)
	echo := sshtest.EchoServer(t)
	jump := sshtest.Start(t, sshtest.Options{Password: "j"})
	target := sshtest.Start(t, sshtest.Options{Password: "t"})
	tg := targetFor(target)
	tg.StrictHostKeyChecking = "accept-new"
	jt := targetFor(jump)
	jt.StrictHostKeyChecking = "accept-new"
	tg.Jumps = []Target{jt}
	tu, err := Open(context.Background(), tg, Callbacks{Secret: adapt(func(_, prompt string) (string, error) {
		if strings.Contains(prompt, jump.Addr[strings.LastIndex(jump.Addr, ":"):]) {
			return "j", nil
		}
		return "t", nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer tu.Close()
	roundTrip(t, tu, echo)
	fw := strings.Join(jump.Forwards(), ",")
	tfw := strings.Join(target.Forwards(), ",")
	if fw != target.Addr || tfw != echo {
		t.Errorf("jump forwarded %q, target forwarded %q", fw, tfw)
	}
}

func TestSocketNotSupported(t *testing.T) {
	tu := &Tunnel{}
	if _, err := tu.DialContext(context.Background(), "unix", "/tmp/x"); err == nil {
		t.Fatal("unix socket through SSH accepted")
	}
}

// TestSlowAnswerDoesNotTimeOut: the handshake deadline is paused while
// the user answers (here longer than ConnectTimeout).
func TestSlowAnswerDoesNotTimeOut(t *testing.T) {
	isolate(t)
	srv := sshtest.Start(t, sshtest.Options{Password: "pw"})
	tg := targetFor(srv)
	tg.ConnectTimeout = 1
	tu, err := Open(context.Background(), tg, Callbacks{
		ConfirmHostKey: func(string) (bool, error) { time.Sleep(1500 * time.Millisecond); return true, nil },
		Secret:         adapt(func(string, string) (string, error) { time.Sleep(1500 * time.Millisecond); return "pw", nil }),
	})
	if err != nil {
		t.Fatalf("slow answers: %v", err)
	}
	tu.Close()
}

// adapt turns a (kind, prompt) answer function into a Secret callback.
func adapt(f func(kind, prompt string) (string, error)) func(SecretRequest) (string, error) {
	return func(r SecretRequest) (string, error) { return f(r.Kind, r.Prompt) }
}
