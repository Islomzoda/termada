package sshx

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/fleet"
	"golang.org/x/crypto/ssh"
)

var tmuxSeq int64

// sshConn bundles one live SSH connection's transport parts. The guard makes
// closing the connection wait for any in-flight Write/Signal to drain, so a
// Reconnect (reader goroutine) can't tear down the ssh channel while a handler
// goroutine is still writing to it — the x/crypto/ssh write path is not safe
// against a concurrent close (use-after-close + a -race-flagged channel race).
type sshConn struct {
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader

	mu     sync.RWMutex // R during write, W during close
	closed bool
}

// write sends to the connection's stdin, blocking a concurrent close until it
// returns; once closed it fails fast instead of touching a torn-down channel.
func (c *sshConn) write(p []byte) (int, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	return c.stdin.Write(p)
}

// sshShell is a persistent interactive shell over an SSH PTY. It satisfies
// engine.ShellConn, so a remote session runs the exact same marker-based exec
// protocol as a local PTY session (spec §14, P-10). The remote shell is wrapped
// in a named tmux session so the work (cwd/env/running command) survives a
// dropped connection; Reconnect re-dials and re-attaches by name (spec RM-3).
type sshShell struct {
	open func() (*sshConn, error) // (re)establish a connection + tmux re-attach

	mu   sync.Mutex
	conn *sshConn
}

func (s *sshShell) current() *sshConn {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// Reconnect re-dials the server and re-attaches the persistent tmux session,
// swapping in the fresh transport. Because the remote shell (and any command it
// was running) survived inside tmux, the session resumes where it left off. If
// the remote has no tmux it falls back to a plain shell — the connection is
// re-established but prior in-flight state is gone (the engine orphans it).
func (s *sshShell) Reconnect() error {
	c, err := s.open()
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.conn
	s.conn = c
	s.mu.Unlock()
	_ = closeConn(old)
	return nil
}

func closeConn(c *sshConn) error {
	if c == nil {
		return nil
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
// interactive login shell on a PTY, wrapped in a reconnect-safe tmux session,
// returned as an engine.ShellConn for a persistent remote session.
func (r *Runner) OpenShell(server fleet.Server, cols, rows int) (engine.ShellConn, error) {
	if cols <= 0 {
		cols = 200
	}
	if rows <= 0 {
		rows = 50
	}
	// Stable tmux session name for this shell's whole lifetime, so a Reconnect
	// re-attaches the same remote session (state preserved). Unique per process
	// + per OpenShell call so concurrent remote sessions never collide.
	name := fmt.Sprintf("termada-%d-%d", os.Getpid(), atomic.AddInt64(&tmuxSeq, 1))

	open := func() (*sshConn, error) {
		client, err := r.dial(server)
		if err != nil {
			return nil, err
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
		// Opportunistically replace the login shell with a tmux session so the
		// remote work survives a dropped connection. If tmux is unavailable the
		// `&&` short-circuits and the plain login shell remains — identical to a
		// non-tmux server. `exec` keeps the marker protocol on the same channel.
		wrap := fmt.Sprintf("command -v tmux >/dev/null 2>&1 && exec tmux new-session -A -s '%s'\n", name)
		if _, err := stdin.Write([]byte(wrap)); err != nil {
			_ = sess.Close()
			_ = client.Close()
			return nil, err
		}
		return &sshConn{client: client, sess: sess, stdin: stdin, stdout: stdout}, nil
	}

	c, err := open()
	if err != nil {
		return nil, err
	}
	return &sshShell{open: open, conn: c}, nil
}
