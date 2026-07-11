package sshx

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/fleet"
	"golang.org/x/crypto/ssh"
)

// sshConn bundles one live SSH connection's transport parts. The guard makes
// closing the connection wait for any in-flight Write/Signal to drain, so a
// Reconnect (reader goroutine) can't tear down the ssh channel while a handler
// goroutine is still writing to it — the x/crypto/ssh write path is not safe
// against a concurrent close (use-after-close + a -race-flagged channel race).
type sshConn struct {
	client       *ssh.Client
	sess         *ssh.Session
	stdin        io.WriteCloser
	stdout       io.Reader
	transport    net.Conn
	writeTimeout time.Duration

	writeMu sync.Mutex   // preserves byte ordering across concurrent callers
	mu      sync.RWMutex // R during write, W during close
	closed  bool
}

type writeResult struct {
	n   int
	err error
}

// write sends to the connection's stdin. The explicit timer is required in
// addition to a TCP write deadline: an SSH channel can block locally waiting
// for a remote window adjustment without performing any socket write.
func (c *sshConn) write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	write := func(payload []byte) writeResult {
		c.mu.RLock()
		defer c.mu.RUnlock()
		if c.closed {
			return writeResult{err: io.ErrClosedPipe}
		}
		if c.transport != nil && c.writeTimeout > 0 {
			if err := c.transport.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
				return writeResult{err: err}
			}
		}
		n, err := c.stdin.Write(payload)
		if c.transport != nil && c.writeTimeout > 0 {
			if clearErr := c.transport.SetWriteDeadline(time.Time{}); err == nil {
				err = clearErr
			}
		}
		return writeResult{n: n, err: err}
	}
	if c.writeTimeout <= 0 {
		result := write(p)
		return result.n, result.err
	}

	// Own the bytes until the worker returns; callers may reuse their buffer as
	// soon as this method times out.
	payload := append([]byte(nil), p...)
	done := make(chan writeResult, 1)
	go func() { done <- write(payload) }()
	timer := time.NewTimer(c.writeTimeout)
	defer timer.Stop()
	select {
	case result := <-done:
		if result.err != nil && c.transport != nil {
			_ = c.transport.Close()
		}
		return result.n, result.err
	case <-timer.C:
		if c.transport != nil {
			// Closing net.Conn is concurrency-safe and wakes the SSH mux, including
			// writers blocked waiting for remote channel-window credit.
			_ = c.transport.Close()
		}
		select {
		case result := <-done:
			return result.n, fmt.Errorf("SSH write timed out after %s", c.writeTimeout)
		case <-time.After(time.Second):
			return 0, fmt.Errorf("SSH write timed out after %s", c.writeTimeout)
		}
	}
}

// sshShell is a persistent interactive shell over an SSH PTY. It satisfies
// engine.ShellConn, so a remote session runs the exact same marker-based exec
// protocol as a local PTY session (spec §14, P-10). On a dropped connection,
// Reconnect re-dials a fresh shell so the session keeps serving commands (spec RM-3).
type sshShell struct {
	open func() (*sshConn, error) // (re)establish a connection + tmux re-attach

	mu     sync.Mutex
	conn   *sshConn
	closed bool
}

func (s *sshShell) current() *sshConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.conn
}

func (s *sshShell) Read(p []byte) (int, error) {
	c := s.current()
	if c == nil {
		return 0, io.EOF
	}
	return c.stdout.Read(p)
}

func (s *sshShell) Write(p []byte) (int, error) {
	c := s.current()
	if c == nil {
		return 0, io.ErrClosedPipe
	}
	return c.write(p)
}

func (s *sshShell) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	return closeConn(c)
}

// Signal interrupts the foreground command. Over SSH we can't read the remote
// foreground pgid, so we send Ctrl-C through the PTY line discipline (which
// delivers SIGINT to the remote foreground group) — best-effort for all signals.
func (s *sshShell) Signal(name string) error {
	switch name {
	case "SIGKILL", "KILL", "SIGINT", "INT", "SIGTERM", "TERM", "":
		c := s.current()
		if c == nil {
			return io.ErrClosedPipe
		}
		_, err := c.write([]byte{0x03})
		return err
	default:
		return fmt.Errorf("signal %s is not supported over SSH", name)
	}
}

// Reconnect re-dials the server and swaps in a fresh shell transport, so the
// session keeps serving new commands after a dropped connection. The in-flight
// command's remote state is not preserved (the engine orphans it).
func (s *sshShell) Reconnect() error {
	c, err := s.open()
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = closeConn(c)
		return io.ErrClosedPipe
	}
	old := s.conn
	s.conn = c
	s.mu.Unlock()
	_ = closeConn(old)
	return nil
}

// keepalive pings the connection every interval; if a ping fails or times out
// (a silently-dropped link with no RST), it closes the conn so the session
// reader sees EOF and reconnects — instead of a Read blocking forever. It stops
// once the conn is closed. interval <= 0 disables it.
func keepalive(c *sshConn, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		c.mu.RLock()
		closed, client := c.closed, c.client
		c.mu.RUnlock()
		if closed || client == nil {
			return
		}
		res := make(chan error, 1)
		go func() {
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			res <- err
		}()
		select {
		case err := <-res:
			if err != nil {
				_ = closeConn(c)
				return
			}
		case <-time.After(interval):
			if c.transport != nil {
				_ = c.transport.Close()
			}
			_ = closeConn(c)
			return
		}
	}
}

func closeConn(c *sshConn) error {
	if c == nil {
		return nil
	}
	// Break a channel writer that is waiting for remote window credit before
	// waiting on its read lock. net.Conn.Close is concurrency-safe and wakes the
	// SSH mux; the channel/session close below remains serialized with writes.
	if c.transport != nil {
		_ = c.transport.Close()
	}
	// Take the write lock so we wait for any in-flight write() to finish and
	// block new ones (they'll see closed==true and bail) before tearing down.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.sess != nil {
		_ = c.sess.Close()
	}
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// OpenShell dials the server (vault creds, TOFU host key) and starts an
// interactive login shell on a PTY, returned as an engine.ShellConn for a
// persistent remote session running the same marker-based exec protocol as a
// local PTY. (A plain shell, not tmux: tmux's screen rendering corrupts the
// completion markers, so a Reconnect re-dials a fresh shell — in-flight remote
// state is not preserved across a drop; the engine orphans the running job.)
func (r *Runner) OpenShell(server fleet.Server, cols, rows int) (engine.ShellConn, error) {
	if cols <= 0 {
		cols = 200
	}
	if rows <= 0 {
		rows = 50
	}
	open := func() (*sshConn, error) {
		client, transport, err := r.dialTransport(server)
		if err != nil {
			return nil, err
		}
		if err := setDeadline(transport, r.ioTimeout); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("set SSH shell setup deadline: %w", err)
		}
		sess, err := client.NewSession()
		if err != nil {
			_ = client.Close()
			return nil, err
		}
		modes := ssh.TerminalModes{ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
		if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
			_ = sess.Close()
			_ = client.Close()
			return nil, err
		}
		stdout, err := sess.StdoutPipe()
		if err != nil {
			_ = sess.Close()
			_ = client.Close()
			return nil, err
		}
		stdin, err := sess.StdinPipe()
		if err != nil {
			_ = sess.Close()
			_ = client.Close()
			return nil, err
		}
		if err := sess.Shell(); err != nil {
			_ = sess.Close()
			_ = client.Close()
			return nil, err
		}
		if err := transport.SetDeadline(time.Time{}); err != nil {
			_ = sess.Close()
			_ = client.Close()
			return nil, fmt.Errorf("clear SSH shell setup deadline: %w", err)
		}
		c := &sshConn{client: client, sess: sess, stdin: stdin, stdout: stdout, transport: transport, writeTimeout: r.ioTimeout}
		go keepalive(c, r.keepalive) // detect a silently-dropped link → reconnect
		return c, nil
	}

	c, err := open()
	if err != nil {
		return nil, err
	}
	return &sshShell{open: open, conn: c}, nil
}
