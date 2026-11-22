package sshtunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SecretRequest describes one question for a secret, so an answer given
// (or stored) for one question is never sent in reply to another.
type SecretRequest struct {
	// Kind is "password" (SSH password, also the password question of
	// keyboard-interactive), "passphrase" (of a key file) or "answer" (any
	// other keyboard-interactive question, e.g. a one-time code).
	Kind string
	// Hop is host:port of the SSH server that asks (for a passphrase: the
	// server the key is used for).
	Hop string
	// Key is the key file, for a passphrase.
	Key string
	// Target is true for the target server, false for a jump host.
	Target bool
	// Prompt is the text to show.
	Prompt string
}

// Secret is a password or passphrase that was entered for the target.
type Secret struct {
	Kind  string // "password" or "passphrase"
	Key   string // key file, for a passphrase
	Value string
}

// Callbacks connect the tunnel to the user.
type Callbacks struct {
	// Secret asks for a password, key passphrase or other answer. Nil: no
	// prompting.
	Secret func(r SecretRequest) (string, error)
	// ConfirmHostKey asks whether to trust an unknown host key (like ssh's
	// "Are you sure you want to continue connecting?"). Nil: unknown host
	// keys are rejected.
	ConfirmHostKey func(msg string) (bool, error)
}

// ErrUnknownHostKey is returned when an unknown host key was not accepted.
var ErrUnknownHostKey = errors.New("unknown SSH host key")

// Tunnel is an SSH connection (with its ProxyJump hops) through which
// database connections are dialled. It reconnects on demand when the SSH
// connection was lost.
type Tunnel struct {
	target Target
	cb     Callbacks

	connSem chan struct{} // one connect at a time (a semaphore, so waiting honours ctx)

	mu      sync.Mutex
	clients []*ssh.Client     // hops, then the target
	secrets map[string]string // answers of successful logins, for reconnects
	used    *Secret           // the secret entered for the target, if any
	closed  bool
}

// Open connects to t (through its jumps).
func Open(ctx context.Context, t Target, cb Callbacks) (*Tunnel, error) {
	tu := &Tunnel{target: t, cb: cb, secrets: map[string]string{}, connSem: make(chan struct{}, 1)}
	if err := tu.connect(ctx); err != nil {
		return nil, err
	}
	return tu, nil
}

// Target returns the SSH target.
func (tu *Tunnel) Target() Target { return tu.target }

// UsedSecret returns the password or passphrase that was entered for the
// target during the last successful login (nil when the agent or an
// unencrypted key was used). Answers to other keyboard-interactive
// questions (one-time codes) are never returned.
func (tu *Tunnel) UsedSecret() *Secret {
	tu.mu.Lock()
	defer tu.mu.Unlock()
	if tu.used == nil {
		return nil
	}
	s := *tu.used
	return &s
}

// Close closes the SSH connection(s).
func (tu *Tunnel) Close() error {
	tu.mu.Lock()
	defer tu.mu.Unlock()
	tu.closed = true
	tu.closeClientsLocked()
	return nil
}

func (tu *Tunnel) closeClientsLocked() {
	for i := len(tu.clients) - 1; i >= 0; i-- {
		tu.clients[i].Close()
	}
	tu.clients = nil
}

// DialContext dials addr (host:port as seen from the SSH server) through
// the tunnel. It reconnects once if the SSH connection was lost. The
// connection supports deadlines (see deadlineConn) and reports addr as its
// remote address.
func (tu *Tunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network == "" || strings.HasPrefix(network, "tcp") {
		network = "tcp"
	} else {
		return nil, fmt.Errorf("ssh tunnel: network %q is not supported (use host and port, not a socket)", network)
	}
	for attempt := 0; ; attempt++ {
		c, err := tu.client(ctx)
		if err != nil {
			return nil, err
		}
		conn, err := dialVia(ctx, c, network, addr)
		if err == nil {
			// The SSH library reports 0.0.0.0:0 as the remote address;
			// PostgreSQL sends its cancel request to RemoteAddr(), so report
			// the address that was dialled (through the tunnel again).
			return newDeadlineConn(conn, dialedAddr{network, addr}), nil
		}
		if attempt > 0 || !tu.lost(ctx, c) {
			return nil, fmt.Errorf("ssh tunnel %s: dial %s: %w", tu.target.String(), addr, err)
		}
	}
}

type dialedAddr struct{ network, addr string }

func (a dialedAddr) Network() string { return a.network }
func (a dialedAddr) String() string  { return a.addr }

// dialVia dials through an SSH client, honouring ctx.
func dialVia(ctx context.Context, c *ssh.Client, network, addr string) (net.Conn, error) {
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		conn, err := c.Dial(network, addr)
		ch <- res{conn, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// lost reports whether c is broken and was dropped (so a reconnect makes
// sense). It waits at most 5 s (or until ctx ends) for the server.
func (tu *Tunnel) lost(ctx context.Context, c *ssh.Client) bool {
	res := make(chan error, 1)
	go func() { _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); res <- err }()
	var err error
	select {
	case err = <-res:
	case <-time.After(5 * time.Second):
		err = errors.New("no keepalive reply")
	case <-ctx.Done():
		return false
	}
	if err == nil {
		return false
	}
	tu.mu.Lock()
	if len(tu.clients) > 0 && tu.clients[len(tu.clients)-1] == c {
		tu.closeClientsLocked()
	}
	tu.mu.Unlock()
	return true
}

func (tu *Tunnel) current() (*ssh.Client, error) {
	tu.mu.Lock()
	defer tu.mu.Unlock()
	if tu.closed {
		return nil, errors.New("ssh tunnel closed")
	}
	if n := len(tu.clients); n > 0 {
		return tu.clients[n-1], nil
	}
	return nil, nil
}

func (tu *Tunnel) client(ctx context.Context) (*ssh.Client, error) {
	if c, err := tu.current(); c != nil || err != nil {
		return c, err
	}
	if err := tu.connect(ctx); err != nil {
		return nil, err
	}
	c, err := tu.current()
	if c == nil && err == nil {
		err = errors.New("ssh tunnel closed")
	}
	return c, err
}

// handshake is the state of one SSH login.
type handshake struct {
	tu       *Tunnel
	hop      Target
	isTarget bool
	conn     net.Conn
	timeout  time.Duration
	ctx      context.Context

	mu      sync.Mutex
	agents  []net.Conn        // agent connections, closed after the login
	pending map[string]string // answers given in this login
	tried   map[string]bool   // remembered answers already offered
	used    *Secret
}

// connect logs in to every hop. Only one connect runs at a time; if
// another one has just rebuilt the tunnel, it returns at once.
func (tu *Tunnel) connect(ctx context.Context) error {
	select {
	case tu.connSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err() // another login (e.g. waiting for an answer) is running
	}
	defer func() { <-tu.connSem }()
	if c, err := tu.current(); c != nil || err != nil {
		return err
	}
	hops := append(slices.Clone(tu.target.Jumps), tu.target)
	var clients []*ssh.Client
	var done []*handshake
	fail := func(err error) error {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
		return err
	}
	for i, h := range hops {
		timeout := time.Duration(h.ConnectTimeout) * time.Second
		if timeout <= 0 {
			timeout = 15 * time.Second
		}
		dctx, cancel := context.WithTimeout(ctx, timeout)
		var conn net.Conn
		var err error
		if i == 0 {
			var d net.Dialer
			conn, err = d.DialContext(dctx, "tcp", h.Addr())
		} else {
			// A channel of the previous hop: wrap it, its own deadlines
			// are not supported.
			var ch net.Conn
			if ch, err = dialVia(dctx, clients[i-1], "tcp", h.Addr()); err == nil {
				conn = newDeadlineConn(ch, nil)
			}
		}
		cancel()
		if err != nil {
			return fail(fmt.Errorf("ssh %s: %w", h.String(), err))
		}
		hs := &handshake{tu: tu, hop: h, isTarget: i == len(hops)-1, conn: conn, timeout: timeout, ctx: ctx,
			pending: map[string]string{}, tried: map[string]bool{}}
		cfg, err := tu.clientConfig(hs)
		if err != nil {
			conn.Close()
			return fail(err)
		}
		// The handshake has no context: bound it with a deadline (paused
		// while the user answers, see userWait) and close the connection
		// when ctx ends.
		conn.SetDeadline(time.Now().Add(timeout))
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		cc, chans, reqs, err := ssh.NewClientConn(conn, h.Addr(), cfg)
		stopped := stop()
		hs.closeAgents()
		if err == nil && !stopped {
			cc.Close() // ctx ended while the login finished
			err = ctx.Err()
		}
		if err != nil {
			conn.Close()
			if ctx.Err() != nil {
				return fail(fmt.Errorf("ssh %s: %w", h.String(), ctx.Err()))
			}
			return fail(describeAuthError(h, err))
		}
		conn.SetDeadline(time.Time{})
		c := ssh.NewClient(cc, chans, reqs)
		clients = append(clients, c)
		done = append(done, hs)
		go keepalive(c, h.ServerAliveInterval)
	}
	tu.mu.Lock()
	defer tu.mu.Unlock()
	if tu.closed {
		return fail(errors.New("ssh tunnel closed"))
	}
	for _, hs := range done {
		hs.mu.Lock()
		for k, v := range hs.pending {
			tu.secrets[k] = v
		}
		if hs.isTarget {
			tu.used = hs.used
		}
		hs.mu.Unlock()
	}
	tu.closeClientsLocked()
	tu.clients = clients
	return nil
}

func (hs *handshake) closeAgents() {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	for _, a := range hs.agents {
		a.Close()
	}
	hs.agents = nil
}

// userWait runs fn (a question to the user) with the handshake deadline
// paused, as ssh does: reading a fingerprint or typing a passphrase does
// not count against ConnectTimeout.
func (hs *handshake) userWait(fn func()) {
	hs.conn.SetDeadline(time.Time{})
	defer func() {
		if hs.ctx.Err() == nil {
			hs.conn.SetDeadline(time.Now().Add(hs.timeout))
		}
	}()
	fn()
}

func describeAuthError(h Target, err error) error {
	var hk *hostKeyError
	if errors.As(err, &hk) {
		return fmt.Errorf("ssh %s: %w", h.String(), hk)
	}
	var ke *knownhosts.KeyError
	if errors.As(err, &ke) {
		return fmt.Errorf("ssh %s: %w", h.String(), err)
	}
	if strings.Contains(err.Error(), "unable to authenticate") {
		return fmt.Errorf("ssh %s: authentication failed (agent, key files and password were tried): %w", h.String(), err)
	}
	return fmt.Errorf("ssh %s: %w", h.String(), err)
}

// keepalive sends keepalive@openssh.com every interval and closes the
// client after three missed replies, so a dead tunnel is noticed and
// rebuilt on the next dial.
func keepalive(c *ssh.Client, interval int) {
	d := time.Duration(interval) * time.Second
	if d <= 0 {
		d = 30 * time.Second
	}
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	t := time.NewTicker(d)
	defer t.Stop()
	missed := 0
	for {
		select {
		case <-done:
			return
		case <-t.C:
			res := make(chan error, 1)
			go func() { _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); res <- err }()
			select {
			case err := <-res:
				if err != nil {
					c.Close()
					return
				}
				missed = 0
			case <-time.After(d):
				missed++
				if missed >= 3 {
					c.Close()
					return
				}
			case <-done:
				return
			}
		}
	}
}

// ---------------------------------------------------------------- config

func (tu *Tunnel) clientConfig(hs *handshake) (*ssh.ClientConfig, error) {
	h := hs.hop
	user := h.User
	if user == "" {
		user = localUser()
	}
	hkcb, knownAlgos, err := tu.hostKeyCallback(hs)
	if err != nil {
		return nil, err
	}
	def := ssh.SupportedAlgorithms()
	all := allAlgorithms()
	cfg := &ssh.ClientConfig{
		User:            user,
		HostKeyCallback: hkcb,
		ClientVersion:   "SSH-2.0-dbcc",
	}
	pick := func(option, spec string, defaults, available []string) ([]string, error) {
		l := applyAlgoSpec(defaults, available, spec)
		if len(l) == 0 {
			return nil, fmt.Errorf("ssh %s: %s %q names no algorithm dbcc supports", h.String(), option, spec)
		}
		return l, nil
	}
	if h.HostKeyAlgorithms != "" {
		if cfg.HostKeyAlgorithms, err = pick("HostKeyAlgorithms", h.HostKeyAlgorithms, def.HostKeys, all.HostKeys); err != nil {
			return nil, err
		}
	} else if len(knownAlgos) > 0 {
		// Prefer the key types already in known_hosts (as OpenSSH does), so
		// a host with an ed25519 entry is not reported as changed because
		// it offered RSA first.
		cfg.HostKeyAlgorithms = append(knownAlgos, slices.DeleteFunc(def.HostKeys, func(a string) bool { return slices.Contains(knownAlgos, a) })...)
	}
	if h.KexAlgorithms != "" {
		if cfg.KeyExchanges, err = pick("KexAlgorithms", h.KexAlgorithms, def.KeyExchanges, all.KeyExchanges); err != nil {
			return nil, err
		}
	}
	if h.Ciphers != "" {
		if cfg.Ciphers, err = pick("Ciphers", h.Ciphers, def.Ciphers, all.Ciphers); err != nil {
			return nil, err
		}
	}
	if h.MACs != "" {
		if cfg.MACs, err = pick("MACs", h.MACs, def.MACs, all.MACs); err != nil {
			return nil, err
		}
	}
	cfg.Auth = hs.authMethods(user)
	return cfg, nil
}

func allAlgorithms() ssh.Algorithms {
	s, in := ssh.SupportedAlgorithms(), ssh.InsecureAlgorithms()
	return ssh.Algorithms{
		KeyExchanges:   append(s.KeyExchanges, in.KeyExchanges...),
		Ciphers:        append(s.Ciphers, in.Ciphers...),
		MACs:           append(s.MACs, in.MACs...),
		HostKeys:       append(s.HostKeys, in.HostKeys...),
		PublicKeyAuths: append(s.PublicKeyAuths, in.PublicKeyAuths...),
	}
}

// applyAlgoSpec implements OpenSSH's algorithm list syntax: "a,b" replaces
// the defaults, "+a,b" appends, "-a,b" removes, "^a,b" prepends. Names
// this library does not implement are dropped; wildcards are matched.
func applyAlgoSpec(defaults, available []string, spec string) []string {
	op := byte(0)
	if spec != "" && strings.ContainsRune("+-^", rune(spec[0])) {
		op, spec = spec[0], spec[1:]
	}
	var named []string
	for _, n := range strings.Split(spec, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		for _, a := range available {
			if ok, _ := matchGlob(n, a); ok && !slices.Contains(named, a) {
				named = append(named, a)
			}
		}
	}
	base := slices.Clone(defaults)
	switch op {
	case '+':
		for _, n := range named {
			if !slices.Contains(base, n) {
				base = append(base, n)
			}
		}
		return base
	case '-':
		return slices.DeleteFunc(base, func(a string) bool { return slices.Contains(named, a) })
	case '^':
		rest := slices.DeleteFunc(base, func(a string) bool { return slices.Contains(named, a) })
		return append(named, rest...)
	}
	return named
}
