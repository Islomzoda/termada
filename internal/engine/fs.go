package engine

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/termada/termada/internal/errs"
)

const (
	defaultFileReadBytes = 100_000
	maxFileReadBytes     = 1 << 20
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
	return m.canonicalPathProtected(canonicalPath(path))
}

func (m *Manager) canonicalPathProtected(target string) bool {
	for _, p := range m.protectedPaths {
		if pathWithin(target, p) {
			return true
		}
	}
	return false
}

func pathWithin(target, root string) bool {
	target = filepath.Clean(target)
	root = filepath.Clean(root)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		target = strings.ToLower(target)
		root = strings.ToLower(root)
	}
	if target == root {
		return true
	}
	return pathPrefixWithin(target, root, string(filepath.Separator))
}

// pathPrefixWithin compares already-cleaned paths at a separator boundary. A
// filesystem root already ends in its separator ("/", "C:\\", or a UNC share
// root), so it must not receive a second separator before prefix matching.
func pathPrefixWithin(target, root, separator string) bool {
	prefix := root
	if !strings.HasSuffix(prefix, separator) {
		prefix += separator
	}
	return strings.HasPrefix(target, prefix)
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

// sessionTargetFor resolves a session without leaking cross-agent ids. Empty
// session means the caller's local default and therefore has no remote target.
func (m *Manager) sessionTargetFor(owner, id string) (string, bool, error) {
	if id == "" {
		return "", false, nil
	}
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil || (owner != "" && s.Owner != owner) {
		return "", false, errs.New(errs.NotFound, "session %s not found", id)
	}
	return s.Target, true, nil
}

// FileReadFor is the agent-facing, owner-scoped file read path.
func (m *Manager) FileReadFor(owner, session, path string, maxBytes int) (*FileReadResult, error) {
	target, found, err := m.sessionTargetFor(owner, session)
	if err != nil {
		return nil, err
	}
	return m.fileReadAtTarget(target, found, session, path, maxBytes)
}

// FileWriteFor is the agent-facing, owner-scoped file write path.
func (m *Manager) FileWriteFor(owner, session, path, content, mode string) (*FileWriteResult, error) {
	target, found, err := m.sessionTargetFor(owner, session)
	if err != nil {
		return nil, err
	}
	return m.fileWriteAtTarget(target, found, session, path, content, mode)
}

// FileReadAt reads path for the given session: locally on the daemon host for a
// local/default session, or over SFTP on the session's server for a remote one.
// Secrets are redacted in either case.
func (m *Manager) FileReadAt(session, path string, maxBytes int) (*FileReadResult, error) {
	target, found := m.SessionTarget(session)
	return m.fileReadAtTarget(target, found, session, path, maxBytes)
}

func (m *Manager) fileReadAtTarget(target string, found bool, session, path string, maxBytes int) (*FileReadResult, error) {
	maxBytes, err := normalizeFileReadLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	if found && target != "" && target != "local" {
		if m.remoteFiles == nil {
			return nil, errs.New(errs.NotSupported,
				"remote file ops need the termada daemon (none running); read/write remote files with exec_run in that session")
		}
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
	target, found := m.SessionTarget(session)
	return m.fileWriteAtTarget(target, found, session, path, content, mode)
}

func (m *Manager) fileWriteAtTarget(target string, found bool, session, path, content, mode string) (*FileWriteResult, error) {
	if found && target != "" && target != "local" {
		if m.remoteFiles == nil {
			return nil, errs.New(errs.NotSupported,
				"remote file ops need the termada daemon (none running); read/write remote files with exec_run in that session")
		}
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
	if m.spawn.SeparateUID {
		return nil, errs.New(errs.NotSupported, "local file_read is disabled with security.run_as because file tools execute in the daemon process; use exec in the dropped-uid session")
	}
	maxBytes, err := normalizeFileReadLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	target := canonicalPath(path)
	if m.canonicalPathProtected(target) {
		return nil, errs.New(errs.DeniedByPolicy, "reading %q is denied: protected secret path", path)
	}
	fi, err := os.Stat(target)
	if err != nil {
		return nil, errs.New(errs.NotFound, "%v", err)
	}
	if fi.IsDir() {
		return nil, errs.New(errs.InvalidArgument, "%s is a directory", path)
	}
	if !fi.Mode().IsRegular() {
		return nil, errs.New(errs.InvalidArgument, "%s is not a regular file", path)
	}
	f, err := openLocalNoSymlink(target, false, false)
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

func normalizeFileReadLimit(maxBytes int) (int, error) {
	if maxBytes <= 0 {
		return defaultFileReadBytes, nil
	}
	if maxBytes > maxFileReadBytes {
		return 0, errs.New(errs.InvalidArgument, "max_bytes exceeds %d byte file-read limit", maxFileReadBytes)
	}
	return maxBytes, nil
}

// FileWrite writes content to a local file (spec FS-2). mode "append" appends;
// anything else truncates.
func (m *Manager) FileWrite(path, content, mode string) (*FileWriteResult, error) {
	if m.spawn.SeparateUID {
		return nil, errs.New(errs.NotSupported, "local file_write is disabled with security.run_as because file tools execute in the daemon process; use exec in the dropped-uid session")
	}
	target := canonicalPath(path)
	if m.canonicalPathProtected(target) {
		return nil, errs.New(errs.DeniedByPolicy, "writing %q is denied: protected secret path", path)
	}
	// 0o600 (not 0o644): a file the agent writes may carry secrets/config and
	// must not be world-readable. Only applies when creating the file; an existing
	// file keeps its mode.
	f, err := openLocalNoSymlink(target, true, mode == "append")
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
