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

func TestProtectedRootCoversAllAbsolutePaths(t *testing.T) {
	m := NewManager(DefaultConfig())
	m.SetProtectedPaths([]string{string(filepath.Separator)})
	if !m.pathProtected(filepath.Join(string(filepath.Separator), "tmp", "anything")) {
		t.Fatal("protected filesystem root did not cover a descendant")
	}
}

func TestWindowsDriveRootPrefixIsPortable(t *testing.T) {
	if !pathPrefixWithin(`c:\windows\system32`, `c:\`, `\`) {
		t.Fatal(`protected drive root C:\ did not cover a descendant`)
	}
	if pathPrefixWithin(`d:\windows\system32`, `c:\`, `\`) {
		t.Fatal(`protected drive root C:\ covered a different drive`)
	}
	if pathPrefixWithin(`c:relative`, `c:\`, `\`) {
		t.Fatal(`protected drive root C:\ covered a drive-relative path`)
	}
}

func TestDarwinProtectedPathCaseFolding(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case-insensitive protected-path regression is Darwin-specific")
	}
	root := t.TempDir()
	protected := filepath.Join(root, "secret")
	if err := os.Mkdir(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	alternate := filepath.Join(root, "SECRET", "token")
	if _, err := os.Stat(filepath.Dir(alternate)); err != nil {
		t.Skip("test volume is case-sensitive")
	}
	m := NewManager(DefaultConfig())
	m.SetProtectedPaths([]string{protected})
	if !m.pathProtected(alternate) {
		t.Fatalf("alternate-case path %q bypassed protected root %q", alternate, protected)
	}
}

func TestSecureOpenRejectsSymlinkSwapAfterCanonicalization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix descriptor-walk test")
	}
	root := t.TempDir()
	secret := filepath.Join(root, "secret")
	if err := os.Mkdir(secret, 0o700); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(secret, "token")
	if err := os.WriteFile(secretFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("leaf swap", func(t *testing.T) {
		leaf := filepath.Join(root, "leaf")
		if err := os.WriteFile(leaf, []byte("open"), 0o600); err != nil {
			t.Fatal(err)
		}
		canonical := canonicalPath(leaf)
		if err := os.Remove(leaf); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secretFile, leaf); err != nil {
			t.Fatal(err)
		}
		if f, err := openLocalNoSymlink(canonical, false, false); err == nil {
			_ = f.Close()
			t.Fatal("leaf symlink swap was followed")
		}
	})

	t.Run("parent swap", func(t *testing.T) {
		parent := filepath.Join(root, "parent")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(parent, "token")
		if err := os.WriteFile(path, []byte("open"), 0o600); err != nil {
			t.Fatal(err)
		}
		canonical := canonicalPath(path)
		if err := os.Rename(parent, parent+"-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, parent); err != nil {
			t.Fatal(err)
		}
		if f, err := openLocalNoSymlink(canonical, false, false); err == nil {
			_ = f.Close()
			t.Fatal("parent symlink swap was followed")
		}
	})
}

func TestFileReadRejectsUnboundedAllocation(t *testing.T) {
	m := NewManager(DefaultConfig())
	if _, err := m.FileRead(filepath.Join(t.TempDir(), "missing"), int(^uint(0)>>1)); err == nil {
		t.Fatal("oversized max_bytes was accepted")
	} else if e, ok := err.(*errs.Error); !ok || e.Code != errs.InvalidArgument {
		t.Fatalf("error = %v, want invalid_argument", err)
	}
}

func TestLocalFileToolsFailClosedWithUIDSeparation(t *testing.T) {
	m := NewManager(DefaultConfig())
	m.SetSpawnConfig(SpawnConfig{SeparateUID: true, UID: 12345, GID: 12345})
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.FileRead(path, 100); err == nil {
		t.Fatal("root-daemon file_read remained enabled with uid separation")
	} else if e, ok := err.(*errs.Error); !ok || e.Code != errs.NotSupported {
		t.Fatalf("file_read error = %v, want not_supported", err)
	}
	if _, err := m.FileWrite(path, "changed", ""); err == nil {
		t.Fatal("root-daemon file_write remained enabled with uid separation")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "secret" {
		t.Fatalf("file changed despite fail-closed guard: %q, %v", got, err)
	}
}
