//go:build unix

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"github.com/termada/termada/internal/errs"
)

// Signal delivers a signal to the running foreground command's process group
// (spec EX-5/§18b). Returns not_found if only the session shell is running.
func (p *ptyShell) Signal(name string) error {
	sig, e := mapSignal(name)
	if e != nil {
		return e
	}
	pgid, err := foregroundPgid(p.f.Fd())
	if err != nil {
		return errs.New(errs.Internal, "foreground pgid: %v", err)
	}
	if pgid == p.pid() {
		return errs.New(errs.NotFound, "no command is currently running")
	}
	return killGroup(pgid, sig)
}

// shellPath is the shell used for persistent-shell sessions. bash is required
// for `set -m` (job control) which lets us signal a running command's process
// group without killing the session shell.
const shellPath = "/bin/bash"

// startShell spawns the session shell on a fresh PTY. creack/pty.Start sets the
// shell as a session leader (setsid) with the PTY as its controlling terminal,
// which is what makes job control usable.
//
// When sp.SeparateUID is set the shell is dropped to a less-privileged uid/gid
// (the daemon must be root) so an agent's `exec` runs without access to the
// daemon's secrets, the control socket, or the operator's credential stores
// (SEC-8). pty.Start only adds Setsid/Setctty to an existing SysProcAttr, so the
// Credential we set here is preserved; the slave fds are opened as root and
// inherited, so the dropped child can still use the PTY.
func startShell(cols, rows int, sp SpawnConfig) (*ptyShell, error) {
	cmd := exec.Command(shellPath)
	cmd.Env = shellEnvironment(sp)
	if sp.SeparateUID {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid: uint32(sp.UID),
				Gid: uint32(sp.GID),
				// Drop the daemon's supplementary groups: the agent shell gets only
				// its own primary group, so group-readable secrets stay out of reach.
				Groups: []uint32{uint32(sp.GID)},
			},
		}
	}
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

// shellEnvironment deliberately does not inherit the daemon environment for a
// dropped-uid shell. The daemon commonly receives vault/config tokens via env;
// passing os.Environ through setuid would hand those values straight back to an
// untrusted agent via `env`, defeating the security.run_as boundary.
func shellEnvironment(sp SpawnConfig) []string {
	base := []string{
		"PS1=", "PS2=", // suppress interactive prompts
		"TERM=xterm-256color",
	}
	if !sp.SeparateUID {
		return append(os.Environ(), base...)
	}
	username := sp.Username
	if username == "" || strings.ContainsAny(username, "=\x00") {
		username = strconv.Itoa(sp.UID)
	}
	home := sp.HomeDir
	if !filepath.IsAbs(home) || strings.ContainsRune(home, 0) {
		home = "/"
	}
	return append(base,
		"HOME="+home,
		"USER="+username,
		"LOGNAME="+username,
		"SHELL="+shellPath,
		"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"LANG=C",
		"LC_ALL=C",
	)
}
