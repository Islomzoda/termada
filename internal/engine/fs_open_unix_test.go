//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package engine

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLocalFileToolsRejectFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(DefaultConfig())
	if _, err := m.FileRead(path, 10); err == nil {
		t.Fatal("file_read accepted a FIFO")
	}
	if _, err := m.FileWrite(path, "data", ""); err == nil {
		t.Fatal("file_write accepted a FIFO")
	}
}
