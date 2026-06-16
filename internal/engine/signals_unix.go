//go:build unix

package engine

import (
	"syscall"

	"github.com/termada/termada/internal/errs"
	"golang.org/x/sys/unix"
)

// foregroundPgid returns the foreground process group of the terminal behind fd
// (the PTY master). Under job control (`set -m`) this is the running command's
// own group, distinct from the session shell's group.
func foregroundPgid(fd uintptr) (int, error) {
	return unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
}

// killGroup sends sig to an entire process group (spec §18b: reap the whole
// tree, not just the leader).
func killGroup(pgid int, sig syscall.Signal) error {
	return syscall.Kill(-pgid, sig)
}

// mapSignal converts the spec's signal enum to a syscall signal.
func mapSignal(name string) (syscall.Signal, *errs.Error) {
	switch name {
	case "SIGTERM", "TERM", "":
		return syscall.SIGTERM, nil
	case "SIGKILL", "KILL":
		return syscall.SIGKILL, nil
	case "SIGINT", "INT":
		return syscall.SIGINT, nil
	case "SIGHUP", "HUP":
		return syscall.SIGHUP, nil
	default:
		return 0, errs.New(errs.NotSupported, "unsupported signal %q", name)
	}
}
