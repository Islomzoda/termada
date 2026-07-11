//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package engine

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openSnapshotSource(path string, expected os.FileInfo) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	actual, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		_ = f.Close()
		return nil, fmt.Errorf("snapshot source %q changed during copy", path)
	}
	return f, nil
}
