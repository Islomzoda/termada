package engine

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/termada/termada/internal/errs"
)

// SetProtectedPaths installs the set of paths file_read/file_write must refuse,
// canonicalizing each (absolute + symlink-resolved) so traversal and symlinked
// parents can't slip past (spec C2/FS-3). A path that doesn't exist yet is kept
// in cleaned absolute form so a later file_write can't create a secret under it.
func (m *Manager) SetProtectedPaths(paths []string) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		out = append(out, canonicalPath(p))
	}
	m.protectedPaths = out
}

// pathProtected reports whether path resolves to (or under) a protected prefix.
func (m *Manager) pathProtected(path string) bool {
	target := canonicalPath(path)
	for _, p := range m.protectedPaths {
		if target == p || strings.HasPrefix(target, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// canonicalPath resolves p to an absolute, symlink-free path. For a path whose
// leaf (or deeper) doesn't exist, it resolves the nearest existing ancestor and
// re-appends the remainder — so `../` traversal and a symlinked parent are
// defeated even for a not-yet-created target.
func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	dir, rest := filepath.Dir(abs), filepath.Base(abs)
	for dir != filepath.Dir(dir) { // walk up until the filesystem root
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = filepath.Dir(dir)
	}
	return filepath.Clean(abs)
}

// EnsureLocalFileOp guards a file op against a remote session: it errors only
// when the session targets a remote host AND no remote file backend is wired
// (the in-process fallback). With the daemon's SFTP backend present, remote file
// ops are supported and this passes. A local/empty session always passes.
func (m *Manager) EnsureLocalFileOp(session string) error {
	if target, ok := m.SessionTarget(session); ok && target != "" && target != "local" {
		if m.remoteFiles != nil {
			return nil
		}
		return errs.New(errs.NotSupported,
			"remote file ops need the termada daemon (none running); read/write remote files with exec_run in that session (e.g. command=[\"cat\",\"<path>\"] or [\"tee\",\"<path>\"])")
	}
	return nil
}

// FileReadAt reads path for the given session: locally on the daemon host for a
// local/default session, or over SFTP on the session's server for a remote one.
// Secrets are redacted in either case.
func (m *Manager) FileReadAt(session, path string, maxBytes int) (*FileReadResult, error) {
	if err := m.EnsureLocalFileOp(session); err != nil {
		return nil, err
	}
	if target, ok := m.SessionTarget(session); ok && target != "" && target != "local" {
		content, size, truncated, err := m.remoteFiles.ReadFile(target, path, maxBytes)
		if err != nil {
			return nil, errs.New(errs.Internal, "remote read on %s: %v", target, err)
		}
		s := string(content)
		if m.redactor != nil {
			s = m.redactor.Redact(s)
		}
		return &FileReadResult{Content: s, Truncated: truncated, Size: size}, nil
	}
	return m.FileRead(path, maxBytes)
}

// FileWriteAt writes path for the given session: locally on the daemon host, or
// over SFTP on the session's server for a remote session.
func (m *Manager) FileWriteAt(session, path, content, mode string) (*FileWriteResult, error) {
	if err := m.EnsureLocalFileOp(session); err != nil {
		return nil, err
	}
	if target, ok := m.SessionTarget(session); ok && target != "" && target != "local" {
		n, err := m.remoteFiles.WriteFile(target, path, content, mode)
		if err != nil {
			return nil, errs.New(errs.Internal, "remote write on %s: %v", target, err)
		}
		return &FileWriteResult{OK: true, Bytes: n}, nil
	}
	return m.FileWrite(path, content, mode)
}

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
	if m.pathProtected(path) {
		return nil, errs.New(errs.DeniedByPolicy, "reading %q is denied: protected secret path", path)
	}
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
	if m.pathProtected(path) {
		return nil, errs.New(errs.DeniedByPolicy, "writing %q is denied: protected secret path", path)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if mode == "append" {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	// 0o600 (not 0o644): a file the agent writes may carry secrets/config and
	// must not be world-readable. Only applies when creating the file; an existing
	// file keeps its mode.
	f, err := os.OpenFile(path, flags, 0o600)
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
