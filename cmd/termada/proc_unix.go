//go:build unix

package main

import "syscall"

// detachAttr makes a spawned daemon survive the parent (the shim) exiting, by
// putting it in its own session.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
