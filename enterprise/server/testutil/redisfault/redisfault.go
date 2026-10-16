// Package redisfault injects "command executed, reply lost" faults into a
// Redis client's connections, by wrapping the client's dialer.
//
// A fault is armed for requests containing a byte pattern (e.g. a script's
// SHA1). The matching request is sent to Redis, so Redis executes it, but the
// reply is consumed and discarded, the connection is closed, and the client
// sees the configured error, as if the connection had been reset (or a read
// had timed out) after Redis ran the command.
package redisfault

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Mode selects the error that the client sees in place of the reply.
type Mode int

const (
	// EOF: the server closed the connection.
	EOF Mode = iota
	// Reset: the connection was reset (ECONNRESET).
	Reset
	// Timeout: the read timed out.
	Timeout
)

func (m Mode) String() string {
	switch m {
	case EOF:
		return "EOF"
	case Reset:
		return "ECONNRESET"
	case Timeout:
		return "read timeout"
	}
	return "unknown"
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout (injected)" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// Injector wraps a dialer and injects faults into the connections it makes.
type Injector struct {
	mu        sync.Mutex
	patterns  [][]byte
	remaining int
	mode      Mode

	// Sent counts matching requests written since arming, i.e. how many
	// times the client sent the command.
	sent atomic.Int32
	// Lost counts replies that were discarded.
	lost atomic.Int32
}

// New returns an injector with no fault armed.
func New() *Injector {
	return &Injector{}
}

// Arm makes the next n requests containing all of the given byte patterns
// lose their replies, and resets the counters. Calling it with no patterns
// disarms the injector.
func (in *Injector) Arm(n int, mode Mode, patterns ...[]byte) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.patterns = patterns
	in.remaining = n
	in.mode = mode
	in.sent.Store(0)
	in.lost.Store(0)
}

// Sent returns the number of requests matching the armed patterns that the
// client wrote since the last Arm call.
func (in *Injector) Sent() int { return int(in.sent.Load()) }

// Lost returns the number of replies discarded since the last Arm call.
func (in *Injector) Lost() int { return int(in.lost.Load()) }

func (in *Injector) onWrite(b []byte) (poison bool, mode Mode) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.patterns) == 0 {
		return false, 0
	}
	for _, p := range in.patterns {
		if !bytes.Contains(b, p) {
			return false, 0
		}
	}
	in.sent.Add(1)
	if in.remaining <= 0 {
		return false, 0
	}
	in.remaining--
	return true, in.mode
}

// WrapDialer returns a dialer that dials with d (or a plain net.Dialer if d
// is nil) and wraps the resulting connections.
func (in *Injector) WrapDialer(d func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if d == nil {
		nd := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 5 * time.Minute}
		d = nd.DialContext
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &conn{Conn: c, in: in}, nil
	}
}

type conn struct {
	net.Conn
	in       *Injector
	poisoned atomic.Bool
	mode     atomic.Int32
}

func (c *conn) Write(b []byte) (int, error) {
	if poison, mode := c.in.onWrite(b); poison {
		c.mode.Store(int32(mode))
		c.poisoned.Store(true)
	}
	return c.Conn.Write(b)
}

func (c *conn) Read(b []byte) (int, error) {
	if !c.poisoned.Load() {
		return c.Conn.Read(b)
	}
	// Wait for Redis to reply (proving that it executed the command), then
	// discard the reply and break the connection.
	_ = c.Conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 64<<10)
	n, _ := c.Conn.Read(buf)
	if n > 0 {
		c.in.lost.Add(1)
	}
	c.Conn.Close()
	switch Mode(c.mode.Load()) {
	case Reset:
		return 0, &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	case Timeout:
		return 0, &net.OpError{Op: "read", Net: "tcp", Err: timeoutError{}}
	default:
		return 0, io.EOF
	}
}
