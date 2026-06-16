package engine

import (
	"io"
	"os"

	"github.com/termada/termada/internal/errs"
)

// FileReadResult is returned by FileRead.
type FileReadResult struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Size      int64  `json:"size"`
}

// FileWriteResult is returned by FileWrite.
type FileWriteResult struct {
	OK    bool `json:"ok"`
	Bytes int  `json:"bytes"`
}

// FileRead reads a local file up to maxBytes, redacting secrets in the result
// (spec FS-1/§3a). Reading more than maxBytes sets Truncated.
func (m *Manager) FileRead(path string, maxBytes int) (*FileReadResult, error) {
	if maxBytes <= 0 {
		maxBytes = 100_000
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, errs.New(errs.NotFound, "%v", err)
	}
	if fi.IsDir() {
		return nil, errs.New(errs.InvalidArgument, "%s is a directory", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errs.New(errs.Internal, "%v", err)
	}
	defer f.Close()
	buf := make([]byte, maxBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, errs.New(errs.Internal, "%v", err)
	}
	truncated := n > maxBytes
	if truncated {
		n = maxBytes
	}
	content := string(buf[:n])
	if m.redactor != nil {
		content = m.redactor.Redact(content)
	}
	return &FileReadResult{Content: content, Truncated: truncated, Size: fi.Size()}, nil
}

// FileWrite writes content to a local file (spec FS-2). mode "append" appends;
// anything else truncates.
func (m *Manager) FileWrite(path, content, mode string) (*FileWriteResult, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if mode == "append" {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, errs.New(errs.Internal, "%v", err)
	}
	defer f.Close()
	n, err := f.WriteString(content)
	if err != nil {
		return nil, errs.New(errs.Internal, "%v", err)
	}
	return &FileWriteResult{OK: true, Bytes: n}, nil
}
