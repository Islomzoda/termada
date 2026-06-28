package sshx

import (
	"fmt"
	"io"
	"os"

	"github.com/pkg/sftp"
	"github.com/termada/termada/internal/fleet"
)

// SFTPRead reads up to maxBytes from path on server over SFTP — binary-safe,
// unlike a cat/base64 round-trip. It returns the content, the file's full size,
// and whether it was truncated.
func (r *Runner) SFTPRead(server fleet.Server, path string, maxBytes int) ([]byte, int64, bool, error) {
	if maxBytes <= 0 {
		maxBytes = 100_000
	}
	client, err := r.dial(server)
	if err != nil {
		return nil, 0, false, err
	}
	defer client.Close()
	sc, err := sftp.NewClient(client)
	if err != nil {
		return nil, 0, false, fmt.Errorf("open sftp: %w", err)
	}
	defer sc.Close()

	fi, err := sc.Stat(path)
	if err != nil {
		return nil, 0, false, err
	}
	if fi.IsDir() {
		return nil, 0, false, fmt.Errorf("%s is a directory", path)
	}
	f, err := sc.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()

	buf := make([]byte, maxBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, 0, false, err
	}
	truncated := n > maxBytes
	if truncated {
		n = maxBytes
	}
	return buf[:n], fi.Size(), truncated, nil
}

// SFTPWrite writes content to path on server over SFTP. mode "append" appends;
// anything else truncates. A newly written (non-append) file is set to 0o600 —
// it may carry secrets/config and must not be world-readable.
func (r *Runner) SFTPWrite(server fleet.Server, path, content, mode string) (int, error) {
	client, err := r.dial(server)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	sc, err := sftp.NewClient(client)
	if err != nil {
		return 0, fmt.Errorf("open sftp: %w", err)
	}
	defer sc.Close()

	flags := os.O_CREATE | os.O_WRONLY
	if mode == "append" {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := sc.OpenFile(path, flags)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if mode != "append" {
		_ = sc.Chmod(path, 0o600)
	}
	n, err := f.Write([]byte(content))
	if err != nil {
		return 0, err
	}
	return n, nil
}
