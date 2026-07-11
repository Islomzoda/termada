//go:build windows

package engine

import (
	"fmt"
	"os"
)

func openSnapshotSource(path string, expected os.FileInfo) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	// Checking the opened handle before reading prevents a pathname swap from
	// copying a different file. Windows reparse-point hardening can be added with
	// CreateFile(FILE_FLAG_OPEN_REPARSE_POINT) without changing this contract.
	if !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		_ = f.Close()
		return nil, fmt.Errorf("snapshot source %q changed during copy", path)
	}
	return f, nil
}
