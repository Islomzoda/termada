package engine

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/ids"
)

// maxSnapshotBytes bounds how large a tree we will snapshot (spec §19/R8:
// snapshots are deliberately scoped to local files/dirs — there is no general
// undo for database/network/remote effects).
const maxSnapshotBytes = 200 << 20 // 200 MiB

// SnapshotInfo describes a saved snapshot.
type SnapshotInfo struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Bytes       int64  `json:"bytes"`
	CreatedUnix int64  `json:"created_unix"`
}

// SetSnapshotDir sets where snapshots are stored.
func (m *Manager) SetSnapshotDir(dir string) {
	m.mu.Lock()
	m.snapshotDir = dir
	m.mu.Unlock()
}

func (m *Manager) snapDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotDir
}

// SnapshotCreate copies a local file or directory into the snapshot store so it
// can be restored after a risky operation.
func (m *Manager) SnapshotCreate(source string) (*SnapshotInfo, error) {
	dir := m.snapDir()
	if dir == "" {
		return nil, errs.New(errs.NotSupported, "snapshots are not configured")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return nil, errs.New(errs.InvalidArgument, "%v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, errs.New(errs.NotFound, "%v", err)
	}
	id := ids.New("snap")
	dst := filepath.Join(dir, id)
	n, err := copyTree(abs, filepath.Join(dst, "payload"))
	if err != nil {
		_ = os.RemoveAll(dst)
		return nil, errs.New(errs.Internal, "%v", err)
	}
	info := SnapshotInfo{ID: id, Source: abs, Bytes: n, CreatedUnix: time.Now().Unix()}
	meta, _ := json.Marshal(info)
	if err := os.WriteFile(filepath.Join(dst, "meta.json"), meta, 0o600); err != nil {
		_ = os.RemoveAll(dst)
		return nil, errs.New(errs.Internal, "%v", err)
	}
	return &info, nil
}

// SnapshotRestore restores a snapshot back over its original source path.
func (m *Manager) SnapshotRestore(id string) error {
	dir := m.snapDir()
	if dir == "" {
		return errs.New(errs.NotSupported, "snapshots are not configured")
	}
	base := filepath.Join(dir, id)
	meta, err := os.ReadFile(filepath.Join(base, "meta.json"))
	if err != nil {
		return errs.New(errs.NotFound, "snapshot %s not found", id)
	}
	var info SnapshotInfo
	if err := json.Unmarshal(meta, &info); err != nil {
		return errs.New(errs.Internal, "%v", err)
	}
	payload := filepath.Join(base, "payload")
	// Restore via a temp sibling then atomic swap, so a failed copy doesn't
	// destroy the original.
	tmp := info.Source + ".termada-restore"
	_ = os.RemoveAll(tmp)
	if _, err := copyTree(payload, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return errs.New(errs.Internal, "%v", err)
	}
	_ = os.RemoveAll(info.Source)
	if err := os.Rename(tmp, info.Source); err != nil {
		return errs.New(errs.Internal, "restore swap: %v", err)
	}
	return nil
}

// SnapshotList returns saved snapshots, newest first.
func (m *Manager) SnapshotList() []SnapshotInfo {
	dir := m.snapDir()
	out := []SnapshotInfo{}
	if dir == "" {
		return out
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := os.ReadFile(filepath.Join(dir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var info SnapshotInfo
		if json.Unmarshal(meta, &info) == nil {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedUnix > out[j].CreatedUnix })
	return out
}

// copyTree copies a file or directory tree from src to dst, enforcing the size
// cap. It returns the number of bytes copied.
func copyTree(src, dst string) (int64, error) {
	var total int64
	info, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, err
		}
		return copyFile(src, dst, info.Mode())
	}
	err = filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !fi.Mode().IsRegular() {
			return nil // skip symlinks/devices
		}
		total += fi.Size()
		if total > maxSnapshotBytes {
			return errs.New(errs.InvalidArgument, "snapshot exceeds %d bytes limit", int64(maxSnapshotBytes))
		}
		_, err = copyFile(path, target, fi.Mode())
		return err
	})
	return total, err
}

func copyFile(src, dst string, mode os.FileMode) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	return io.Copy(out, in)
}
