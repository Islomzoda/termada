package engine

import (
	"os"
	"os/exec"
)

// ptyShell is a persistent shell process attached to a PTY master. The
// platform-specific constructor lives in pty_unix.go / pty_windows.go.
type ptyShell struct {
	f   *os.File  // PTY master
	cmd *exec.Cmd // the shell process
}

// pid returns the shell process id, which (after setsid) is also its process
// group id. A running command under `set -m` gets its own group, so a PTY
// foreground pgid different from this means a command is running.
func (p *ptyShell) pid() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return -1
	}
	return p.cmd.Process.Pid
}

func (p *ptyShell) close() {
	if p.f != nil {
		_ = p.f.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	}
}
