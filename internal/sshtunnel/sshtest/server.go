// Package sshtest is an in-process SSH server with direct-tcpip
// forwarding for tests of SSH tunnels.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Server is a minimal SSH server with direct-tcpip forwarding.
type Server struct {
	Addr    string
	HostKey ssh.Signer
	Ln      net.Listener
	// forwards records the destinations of direct-tcpip channels.
	mu       sync.Mutex
	forwards []string
	conns    []net.Conn
	Logins   atomic.Int32
}

type Options struct {
	Password string
	Key      ssh.PublicKey
	HostKey  ssh.Signer
	// KbdPassword also offers keyboard-interactive with one "Password:"
	// question (as OpenSSH does).
	KbdPassword bool
}

func NewSigner(t *testing.T) ssh.Signer {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func Start(t *testing.T, o Options) *Server {
	t.Helper()
	if o.HostKey == nil {
		o.HostKey = NewSigner(t)
	}
	cfg := &ssh.ServerConfig{}
	if o.Password != "" {
		cfg.PasswordCallback = func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == o.Password {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		}
	}
	if o.KbdPassword && o.Password != "" {
		cfg.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			ans, err := ch("", "", []string{"Password: "}, []bool{false})
			if err == nil && len(ans) == 1 && ans[0] == o.Password {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		}
	}
	if o.Key != nil {
		cfg.PublicKeyCallback = func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if string(k.Marshal()) == string(o.Key.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		}
	}
	cfg.AddHostKey(o.HostKey)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), HostKey: o.HostKey, Ln: ln}
	t.Cleanup(func() { ln.Close(); s.DropAll() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.mu.Unlock()
			go s.serve(c, cfg)
		}
	}()
	return s
}

func (s *Server) DropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
}

func (s *Server) serve(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	s.Logins.Add(1)
	go func() {
		for r := range reqs {
			if r.WantReply {
				r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		d := nc.ExtraData()
		host, rest := sshString(d)
		port := binary.BigEndian.Uint32(rest)
		dest := net.JoinHostPort(host, strconv.Itoa(int(port)))
		s.mu.Lock()
		s.forwards = append(s.forwards, dest)
		s.mu.Unlock()
		target, err := net.Dial("tcp", dest)
		if err != nil {
			nc.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			target.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() { io.Copy(ch, target); ch.CloseWrite() }()
		go func() { io.Copy(target, ch); target.Close() }()
	}
}

// Forwards returns the destinations of the forwarded connections.
func (s *Server) Forwards() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.forwards...)
}

func sshString(b []byte) (string, []byte) {
	n := binary.BigEndian.Uint32(b)
	return string(b[4 : 4+n]), b[4+n:]
}

// echoServer answers each line with "echo: <line>".
func EchoServer(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
				defer c.Close()
				buf := make([]byte, 256)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					c.Write(append([]byte("echo: "), buf[:n]...))
				}
			}()
		}
	}()
	return ln.Addr().String()
}
