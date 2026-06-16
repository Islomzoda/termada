package sshx

import (
	"fmt"
	"io"

	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/fleet"
	"golang.org/x/crypto/ssh"
)

// sshShell is a persistent interactive shell over an SSH PTY. It satisfies
// engine.ShellConn, so a remote session runs the exact same marker-based exec
// protocol as a local PTY session (spec §14, P-10).
type sshShell struct {
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader
}

func (s *sshShell) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *sshShell) Write(p []byte) (int, error) { return s.stdin.Write(p) }

func (s *sshShell) Close() error {
	if s.sess != nil {
		_ = s.sess.Close()
	}
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// Signal interrupts the foreground command. Over SSH we can't read the remote
// foreground pgid, so we send Ctrl-C through the PTY line discipline (which
// delivers SIGINT to the remote foreground group) — best-effort for all signals.
func (s *sshShell) Signal(name string) error {
	switch name {
	case "SIGKILL", "KILL", "SIGINT", "INT", "SIGTERM", "TERM", "":
		_, err := s.stdin.Write([]byte{0x03})
		return err
	default:
		return fmt.Errorf("signal %s is not supported over SSH", name)
	}
}

// OpenShell dials the server (vault creds, TOFU host key) and starts an
// interactive login shell on a PTY, returned as an engine.ShellConn for a
// persistent remote session.
func (r *Runner) OpenShell(server fleet.Server, cols, rows int) (engine.ShellConn, error) {
	if cols <= 0 {
		cols = 200
	}
	if rows <= 0 {
		rows = 50
	}
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
	return &sshShell{client: client, sess: sess, stdin: stdin, stdout: stdout}, nil
}
