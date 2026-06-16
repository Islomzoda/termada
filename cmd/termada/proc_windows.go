//go:build windows

package main

import "syscall"

// detachAttr puts the spawned daemon in a new process group so it survives the
// parent (the shim) exiting.
func detachAttr() *syscall.SysProcAttr {
	const createNewProcessGroup = 0x00000200
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}
