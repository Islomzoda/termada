//go:build windows

package engine

import (
	"syscall"

	"github.com/termada/termada/internal/errs"
)

// Signal delivery on Windows requires Job Objects / CTRL_BREAK rather than POSIX
// process groups (spec §18b); it lands with the ConPTY backend. These stubs let
// the package cross-compile and return a clear error at runtime.

func foregroundPgid(fd uintptr) (int, error) {
	return 0, errs.New(errs.NotSupported, "process-group signals are not supported on Windows yet")
}

func killGroup(pgid int, sig syscall.Signal) error {
	return errs.New(errs.NotSupported, "process-group signals are not supported on Windows yet")
}

func mapSignal(name string) (syscall.Signal, *errs.Error) {
	switch name {
	case "SIGTERM", "TERM", "SIGKILL", "KILL", "":
		return syscall.Signal(0), nil
	default:
		return 0, errs.New(errs.NotSupported, "unsupported signal %q", name)
	}
}
