package sshtunnel

import (
	"net"
	"os"
	"sync"
	"time"
)

// deadlineConn adds deadlines to a connection that has none: SSH channels
// return "deadline not supported" from SetDeadline, which breaks drivers
// that set read/write timeouts (go-mssqldb "connection timeout",
// go-sql-driver readTimeout/writeTimeout, pgx connect and cancel
// fallbacks) and the handshake timeout through a jump host.
//
// Semantics as far as a channel allows: an operation started after its
// deadline fails at once with os.ErrDeadlineExceeded; an operation still
// running when its deadline passes is interrupted by closing the channel
// (a channel read cannot be interrupted otherwise), and reports
// os.ErrDeadlineExceeded. An idle connection with a past deadline stays
// open, as with a TCP connection.
type deadlineConn struct {
	net.Conn
	raddr net.Addr // reported remote address (nil: the channel's)

	mu     sync.Mutex
	rd, wd deadline
	fired  bool // closed because a deadline passed during an operation
}

type deadline struct {
	t        time.Time
	timer    *time.Timer
	inflight int
}

func newDeadlineConn(c net.Conn, raddr net.Addr) *deadlineConn {
	return &deadlineConn{Conn: c, raddr: raddr}
}

func (c *deadlineConn) RemoteAddr() net.Addr {
	if c.raddr != nil {
		return c.raddr
	}
	return c.Conn.RemoteAddr()
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	return c.do(&c.rd, func() (int, error) { return c.Conn.Read(b) })
}

func (c *deadlineConn) Write(b []byte) (int, error) {
	return c.do(&c.wd, func() (int, error) { return c.Conn.Write(b) })
}

func (c *deadlineConn) do(d *deadline, op func() (int, error)) (int, error) {
	c.mu.Lock()
	if c.fired {
		c.mu.Unlock()
		return 0, os.ErrDeadlineExceeded
	}
	if !d.t.IsZero() && !time.Now().Before(d.t) {
		c.mu.Unlock()
		return 0, os.ErrDeadlineExceeded
	}
	d.inflight++
	c.mu.Unlock()
	n, err := op()
	c.mu.Lock()
	d.inflight--
	fired := c.fired
	c.mu.Unlock()
	if err != nil && fired {
		err = os.ErrDeadlineExceeded
	}
	return n, err
}

func (c *deadlineConn) SetDeadline(t time.Time) error {
	c.set(&c.rd, t)
	c.set(&c.wd, t)
	return nil
}

func (c *deadlineConn) SetReadDeadline(t time.Time) error  { c.set(&c.rd, t); return nil }
func (c *deadlineConn) SetWriteDeadline(t time.Time) error { c.set(&c.wd, t); return nil }

func (c *deadlineConn) set(d *deadline, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.t = t
	if t.IsZero() {
		return
	}
	d.timer = time.AfterFunc(time.Until(t), func() {
		c.mu.Lock()
		// Only if this deadline is still the current one and an operation
		// is waiting: interrupt it.
		if d.t.Equal(t) && d.inflight > 0 && !c.fired {
			c.fired = true
			c.mu.Unlock()
			c.Conn.Close()
			return
		}
		c.mu.Unlock()
	})
}

func (c *deadlineConn) Close() error {
	c.mu.Lock()
	for _, d := range []*deadline{&c.rd, &c.wd} {
		if d.timer != nil {
			d.timer.Stop()
			d.timer = nil
		}
	}
	c.mu.Unlock()
	return c.Conn.Close()
}
