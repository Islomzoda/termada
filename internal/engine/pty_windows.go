//go:build windows

package engine

import "errors"

// startShell on Windows is not yet implemented: a correct port needs ConPTY
// (Win10 1809+) plus VT-aware output handling, which is tracked as a later phase
// (spec §26/§31, fork R6: Windows is best-effort in 0.x). The build is provided
// so the rest of the toolchain cross-compiles; persistent-shell execution
// returns a clear error until the ConPTY backend lands.
func startShell(cols, rows int) (*ptyShell, error) {
	return nil, errors.New("local PTY sessions are not yet supported on Windows (ConPTY backend pending)")
}

func (p *ptyShell) Signal(name string) error {
	return errors.New("signals are not supported on Windows yet")
}
