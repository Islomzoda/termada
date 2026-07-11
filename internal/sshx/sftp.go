package sshx

import (
	"fmt"
	"io"
	"os"

	"github.com/pkg/sftp"
	"github.com/termada/termada/internal/fleet"
)

const maxSFTPReadBytes = 1 << 20

// SFTPRead reads up to maxBytes from path on server over SFTP — binary-safe,
// unlike a cat/base64 round-trip. It returns the content, the file's full size,
// and whether it was truncated.
func (r *Runner) SFTPRead(server fleet.Server, path string, maxBytes int) ([]byte, int64, bool, error) {
	select {
	case r.sftpSlots <- struct{}{}:
		defer func() { <-r.sftpSlots }()
	default:
		return nil, 0, false, fmt.Errorf("SFTP connection limit reached; retry later")
	}
	if maxBytes <= 0 {
		maxBytes = 100_000
	}
	if maxBytes > maxSFTPReadBytes {
		return nil, 0, false, fmt.Errorf("max_bytes exceeds %d byte SFTP read limit", maxSFTPReadBytes)
	}
	client, transport, err := r.dialTransport(server)
	if err != nil {
		return nil, 0, false, err
	}
	defer client.Close()
	if err := setDeadline(transport, r.ioTimeout); err != nil {
		return nil, 0, false, fmt.Errorf("set SFTP deadline: %w", err)
	}
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
// anything else truncates. Any newly created file is set to 0o600; an existing
// file keeps its mode in both append and truncate modes.
func (r *Runner) SFTPWrite(server fleet.Server, path, content, mode string) (int, error) {
	select {
	case r.sftpSlots <- struct{}{}:
		defer func() { <-r.sftpSlots }()
	default:
		return 0, fmt.Errorf("SFTP connection limit reached; retry later")
	}
	client, transport, err := r.dialTransport(server)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	if err := setDeadline(transport, r.ioTimeout); err != nil {
		return 0, fmt.Errorf("set SFTP deadline: %w", err)
	}
	sc, err := sftp.NewClient(client)
	if err != nil {
		return 0, fmt.Errorf("open sftp: %w", err)
	}
	defer sc.Close()

	existingFlags := os.O_WRONLY
	if mode == "append" {
		existingFlags |= os.O_APPEND
	} else {
		existingFlags |= os.O_TRUNC
	}
	f, created, err := openSFTPWriteFile(sc, path, existingFlags)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if created {
		if err := f.Chmod(0o600); err != nil {
			_ = f.Close()
			_ = sc.Remove(path)
			return 0, fmt.Errorf("secure new remote file: %w", err)
		}
	}
	n, err := f.Write([]byte(content))
	if err != nil {
		return 0, err
	}
	return n, nil
}

func openSFTPWriteFile(sc *sftp.Client, path string, existingFlags int) (*sftp.File, bool, error) {
	f, err := sc.OpenFile(path, existingFlags)
	if err == nil {
		return f, false, nil
	}
	if !os.IsNotExist(err) {
		return nil, false, err
	}
	createdFlags := existingFlags | os.O_CREATE | os.O_EXCL
	f, err = sc.OpenFile(path, createdFlags)
	if err == nil {
		return f, true, nil
	}
	if os.IsExist(err) {
		// Another writer created the path after our first lookup. Treat it as an
		// existing file so we preserve its mode.
		f, err = sc.OpenFile(path, existingFlags)
		return f, false, err
	}
	return nil, false, err
}
