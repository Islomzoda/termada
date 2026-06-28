package selfupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// VerifyEd25519 accepts a valid signature over the message and rejects a tampered
// message, the wrong key, and a garbage signature.
func TestVerifyEd25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	msg := []byte("hash1  termada_linux_amd64.tar.gz\n")
	sigB64 := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))

	if err := VerifyEd25519(msg, sigB64, pubB64); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := VerifyEd25519([]byte("tampered checksums"), sigB64, pubB64); err == nil {
		t.Fatal("tampered message accepted")
	}
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if err := VerifyEd25519(msg, sigB64, base64.StdEncoding.EncodeToString(otherPub)); err == nil {
		t.Fatal("signature verified against the wrong key")
	}
	if err := VerifyEd25519(msg, "not-base64!!", pubB64); err == nil {
		t.Fatal("garbage signature accepted")
	}
}

// SignEd25519 (release side) and VerifyEd25519 (in-binary) round-trip.
func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	msg := []byte("checksums file contents\n")
	sig, err := SignEd25519(msg, base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := VerifyEd25519(msg, sig, base64.StdEncoding.EncodeToString(pub)); err != nil {
		t.Fatalf("verify of own signature failed: %v", err)
	}
}

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
