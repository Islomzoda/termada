package engine

import (
	"os"
	"os/exec"
)

// ShellConn is the transport for a persistent session shell: a local PTY
// (ptyShell) or a remote SSH shell (sshShell). The marker-based exec protocol in
// session.go runs over either.
type ShellConn interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	// Signal interrupts/kills the running foreground command (no-op error if none).
	Signal(name string) error
}

// ptyShell is a persistent shell process attached to a PTY master. The
// platform-specific constructor and Signal live in pty_unix.go / pty_windows.go.
type ptyShell struct {
	f   *os.File  // PTY master
	cmd *exec.Cmd // the shell process
}

func (p *ptyShell) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *ptyShell) Write(b []byte) (int, error) { return p.f.Write(b) }

// pid returns the shell process id, which (after setsid) is also its process
// group id. A running command under `set -m` gets its own group, so a PTY
// foreground pgid different from this means a command is running.
func (p *ptyShell) pid() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return -1
	}
	return p.cmd.Process.Pid
}

func (p *ptyShell) Close() error {
	if p.f != nil {
		_ = p.f.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	}
	return nil
}
