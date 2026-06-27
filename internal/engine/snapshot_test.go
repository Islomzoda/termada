package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotCreateAndRestore(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "snapshots")
	work := filepath.Join(root, "work")
	_ = os.MkdirAll(work, 0o755)
	_ = os.WriteFile(filepath.Join(work, "a.txt"), []byte("original-a"), 0o644)
	_ = os.WriteFile(filepath.Join(work, "b.txt"), []byte("original-b"), 0o644)

	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetSnapshotDir(store)

	snap, err := m.SnapshotCreate(work)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if snap.ID == "" || snap.Bytes == 0 {
		t.Fatalf("bad snapshot info %+v", snap)
	}

	// Mutate destructively: change a file, delete another, add a third.
	_ = os.WriteFile(filepath.Join(work, "a.txt"), []byte("CORRUPTED"), 0o644)
	_ = os.Remove(filepath.Join(work, "b.txt"))
	_ = os.WriteFile(filepath.Join(work, "c.txt"), []byte("junk"), 0o644)

	if list := m.SnapshotList(); len(list) != 1 || list[0].ID != snap.ID {
		t.Fatalf("list = %+v", list)
	}

	if err := m.SnapshotRestore(snap.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}

	a, _ := os.ReadFile(filepath.Join(work, "a.txt"))
	if string(a) != "original-a" {
		t.Fatalf("a.txt = %q, want original-a", a)
	}
	b, err := os.ReadFile(filepath.Join(work, "b.txt"))
	if err != nil || string(b) != "original-b" {
		t.Fatalf("b.txt not restored: %q err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(work, "c.txt")); !os.IsNotExist(err) {
		t.Fatalf("c.txt should be gone after restore")
	}
}

// A restore id must not traverse out of the snapshot store.
func TestSnapshotRestoreRejectsTraversal(t *testing.T) {
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetSnapshotDir(t.TempDir())
	for _, bad := range []string{"../etc", "..", ".", "a/b", `a\b`, ""} {
		err := m.SnapshotRestore(bad)
		if err == nil {
			t.Fatalf("restore(%q) was accepted, want rejection", bad)
		}
	}
}
