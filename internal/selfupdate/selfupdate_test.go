package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifySHA256(t *testing.T) {
	data := []byte("termada binary contents")
	sum := sha256.Sum256(data)
	hexsum := hex.EncodeToString(sum[:])
	if err := VerifySHA256(data, hexsum); err != nil {
		t.Fatalf("matching checksum should verify: %v", err)
	}
	if err := VerifySHA256(data, "deadbeef"); err == nil {
		t.Fatal("wrong checksum should fail")
	}
}

func TestReplacePreservesExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "termada")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Replace(target, []byte("new binary")); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new binary" {
		t.Fatalf("content = %q, want 'new binary'", got)
	}
	fi, _ := os.Stat(target)
	if fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable bit lost: %v", fi.Mode())
	}
}

func TestChecksumFor(t *testing.T) {
	sums := "abc123  termada_linux_amd64.tar.gz\ndef456  termada_darwin_arm64.tar.gz\n"
	if h, ok := checksumFor(sums, "termada_darwin_arm64.tar.gz"); !ok || h != "def456" {
		t.Fatalf("checksumFor = %q, %v", h, ok)
	}
	if _, ok := checksumFor(sums, "missing.tar.gz"); ok {
		t.Fatal("missing asset should not be found")
	}
}
