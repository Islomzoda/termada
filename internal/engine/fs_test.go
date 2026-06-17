package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/termada/termada/internal/errs"
)

// file_read/file_write must refuse protected secret paths — including via `../`
// traversal and symlinked parents — while leaving unrelated files reachable
// (spec C2/FS-3).
func TestFileProtectedPaths(t *testing.T) {
	root := t.TempDir()
	secretDir := filepath.Join(root, "secret")
	openDir := filepath.Join(root, "open")
	for _, d := range []string{secretDir, openDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secretFile := filepath.Join(secretDir, "cli.token")
	if err := os.WriteFile(secretFile, []byte("super-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	openFile := filepath.Join(openDir, "notes.txt")
	if err := os.WriteFile(openFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(DefaultConfig())
	m.SetProtectedPaths([]string{secretDir})

	denied := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected denial, got nil error")
		}
		if e, ok := err.(*errs.Error); !ok || e.Code != errs.DeniedByPolicy {
			t.Fatalf("expected denied_by_policy, got %v", err)
		}
	}

	t.Run("read inside protected dir is denied", func(t *testing.T) {
		_, err := m.FileRead(secretFile, 0)
		denied(t, err)
	})

	t.Run("write inside protected dir is denied", func(t *testing.T) {
		_, err := m.FileWrite(filepath.Join(secretDir, "new.txt"), "x", "")
		denied(t, err)
		// the write must not have happened
		if _, err := os.Stat(filepath.Join(secretDir, "new.txt")); !os.IsNotExist(err) {
			t.Fatalf("protected file was created despite denial")
		}
	})

	t.Run("traversal into protected dir is denied", func(t *testing.T) {
		sneaky := filepath.Join(openDir, "..", "secret", "cli.token")
		_, err := m.FileRead(sneaky, 0)
		denied(t, err)
	})

	t.Run("symlinked parent into protected dir is denied", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need privilege on Windows")
		}
		link := filepath.Join(root, "link-to-secret")
		if err := os.Symlink(secretDir, link); err != nil {
			t.Fatal(err)
		}
		_, err := m.FileRead(filepath.Join(link, "cli.token"), 0)
		denied(t, err)
	})

	t.Run("unrelated file is readable", func(t *testing.T) {
		res, err := m.FileRead(openFile, 0)
		if err != nil {
			t.Fatalf("expected open file readable, got %v", err)
		}
		if res.Content != "hello" {
			t.Fatalf("content = %q, want hello", res.Content)
		}
	})

	t.Run("exact protected file is denied, sibling is not", func(t *testing.T) {
		sibling := filepath.Join(secretDir, "sibling.txt")
		if err := os.WriteFile(sibling, []byte("ok"), 0o644); err != nil {
			t.Fatal(err)
		}
		m2 := NewManager(DefaultConfig())
		m2.SetProtectedPaths([]string{secretFile}) // protect a single file, not the dir

		if _, err := m2.FileRead(secretFile, 0); err == nil {
			t.Fatal("expected exact-file protection to deny read")
		}
		// a sibling in the same dir must NOT be caught: the prefix match has to
		// respect the path separator boundary, not be a raw string prefix.
		res, err := m2.FileRead(sibling, 0)
		if err != nil {
			t.Fatalf("sibling should be readable, got %v", err)
		}
		if res.Content != "ok" {
			t.Fatalf("sibling content = %q, want ok", res.Content)
		}
	})
}
