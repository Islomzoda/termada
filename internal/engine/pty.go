package engine

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// shellPath is the shell used for persistent-shell sessions. bash is required
// for `set -m` (job control) which lets us signal a running command's process
// group without killing the session shell.
const shellPath = "/bin/bash"

// ptyShell is a persistent shell process attached to a PTY master.
type ptyShell struct {
	f   *os.File  // PTY master
	cmd *exec.Cmd // the shell process
}

// startShell spawns the session shell on a fresh PTY. creack/pty.Start sets the
// shell as a session leader (setsid) with the PTY as its controlling terminal,
// which is what makes job control usable.
func startShell(cols, rows int) (*ptyShell, error) {
	cmd := exec.Command(shellPath)
	cmd.Env = append(os.Environ(),
		"PS1=", "PS2=", // suppress any prompts
		"TERM=xterm-256color",
	)
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	if cols <= 0 {
		cols = 200
	}
	if rows <= 0 {
		rows = 50
	}
	_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	return &ptyShell{f: f, cmd: cmd}, nil
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
