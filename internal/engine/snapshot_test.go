package engine

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSnapshotSingleFileEnforcesSizeCap(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "oversized.bin")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A sparse file exercises the stat-side cap without allocating 200 MiB.
	if err := os.Truncate(source, int64(maxSnapshotBytes)+1); err != nil {
		t.Fatal(err)
	}
	if _, err := copyTree(source, filepath.Join(root, "copy")); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized single-file snapshot error = %v", err)
	}
}

func TestSnapshotRejectsSpecialRootSource(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := copyTree(link, filepath.Join(root, "copy")); err == nil || !strings.Contains(err.Error(), "regular file or directory") {
		t.Fatalf("special snapshot source error = %v", err)
	}
}

func TestSnapshotRejectsNestedSpecialEntry(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "target"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(source, "nested-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := copyTree(source, filepath.Join(root, "copy"))
	if err == nil || !strings.Contains(err.Error(), "nested-link") {
		t.Fatalf("nested special entry error = %v", err)
	}
}

func TestSnapshotCopyRejectsSourceSwapAndDestinationSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(root, "moved")
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("do-not-copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, source); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := copyFile(source, filepath.Join(root, "copy"), expected, 1024); err == nil {
		t.Fatal("source swap to symlink was accepted")
	}

	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, source); err != nil {
		t.Fatal(err)
	}
	expected, _ = os.Lstat(source)
	destination := filepath.Join(root, "destination")
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, destination); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := copyFile(source, destination, expected, 1024); err == nil {
		t.Fatal("destination symlink was accepted")
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "unchanged" {
		t.Fatalf("destination symlink target changed: %q, %v", got, err)
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
