//go:build unix

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
