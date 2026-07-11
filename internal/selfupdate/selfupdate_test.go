package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	sums := a + "  termada_linux_amd64.tar.gz\n" + b + "  termada_darwin_arm64.tar.gz\n"
	if h, ok := checksumFor(sums, "termada_darwin_arm64.tar.gz"); !ok || h != b {
		t.Fatalf("checksumFor = %q, %v", h, ok)
	}
	if _, ok := checksumFor(sums, "missing.tar.gz"); ok {
		t.Fatal("missing asset should not be found")
	}
	if _, ok := checksumFor(a+"  ../../termada_linux_amd64.tar.gz\n", "termada_linux_amd64.tar.gz"); ok {
		t.Fatal("path-confused checksum entry was accepted")
	}
	if _, ok := checksumFor(a+"  termada_linux_amd64.tar.gz\n"+a+"  termada_linux_amd64.tar.gz\n", "termada_linux_amd64.tar.gz"); ok {
		t.Fatal("duplicate checksum entries were accepted")
	}
	if _, ok := checksumFor("bad  termada_linux_amd64.tar.gz\n"+a+"  termada_linux_amd64.tar.gz\n", "termada_linux_amd64.tar.gz"); ok {
		t.Fatal("malformed duplicate checksum entry was ignored")
	}
}

func TestPlatformBinaryName(t *testing.T) {
	if got := platformBinaryName("windows"); got != "termada.exe" {
		t.Fatalf("windows binary = %q", got)
	}
	if got := platformBinaryName("linux"); got != "termada" {
		t.Fatalf("linux binary = %q", got)
	}
}

func TestAutomaticReplacePlatformContract(t *testing.T) {
	if automaticReplaceSupported("windows") {
		t.Fatal("Windows running-executable replacement was claimed as supported")
	}
	for _, goos := range []string{"linux", "darwin"} {
		if !automaticReplaceSupported(goos) {
			t.Fatalf("%s replacement unexpectedly unsupported", goos)
		}
	}
}

func TestSemanticVersionOrderingPreventsDowngrade(t *testing.T) {
	parse := func(value string) semanticVersion {
		t.Helper()
		v, err := parseSemver(value)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"0.7.6", "0.7.5", 1},
		{"0.7.5", "0.7.5", 0},
		{"0.7.5-rc.1", "0.7.5", -1},
		{"1.0.0-alpha.2", "1.0.0-alpha.10", -1},
	} {
		got := compareSemver(parse(tc.a), parse(tc.b))
		if got != tc.want {
			t.Fatalf("compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	for _, invalid := range []string{"", "v1.2.3", "1.2", "1.02.3", "1.2.3-01", "1.2.x", "1.2.3+bad!"} {
		if _, err := parseSemver(invalid); err == nil {
			t.Fatalf("invalid semantic version %q was accepted", invalid)
		}
	}
}

func makeArchive(t *testing.T, hdr *tar.Header, data []byte) []byte {
	return makeArchiveEntries(t, []struct {
		header *tar.Header
		data   []byte
	}{{hdr, data}})
}

func makeArchiveEntries(t *testing.T, entries []struct {
	header *tar.Header
	data   []byte
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		if err := tw.WriteHeader(entry.header); err != nil {
			t.Fatal(err)
		}
		if len(entry.data) > 0 {
			if _, err := tw.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = tw.Close() // oversized-header tests intentionally provide no body
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinaryBoundedAndRegular(t *testing.T) {
	archive := makeArchive(t, &tar.Header{Name: "termada.exe", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg}, []byte("exe"))
	got, err := extractBinary(archive, "termada.exe")
	if err != nil || string(got) != "exe" {
		t.Fatalf("extract windows binary = %q, %v", got, err)
	}

	oversized := makeArchive(t, &tar.Header{Name: "termada", Mode: 0o755, Size: int64(maxBinaryBytes) + 1, Typeflag: tar.TypeReg}, nil)
	if _, err := extractBinary(oversized, "termada"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized binary extraction error = %v", err)
	}

	symlink := makeArchive(t, &tar.Header{Name: "termada", Linkname: "/tmp/evil", Typeflag: tar.TypeSymlink}, nil)
	if _, err := extractBinary(symlink, "termada"); err == nil || !strings.Contains(err.Error(), "not a regular") {
		t.Fatalf("symlink binary extraction error = %v", err)
	}

	nested := makeArchive(t, &tar.Header{Name: "../../termada", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg}, []byte("bad"))
	if _, err := extractBinary(nested, "termada"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("nested binary member error = %v", err)
	}

	duplicate := makeArchiveEntries(t, []struct {
		header *tar.Header
		data   []byte
	}{
		{&tar.Header{Name: "termada", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg}, []byte("one")},
		{&tar.Header{Name: "termada", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg}, []byte("two")},
	})
	if _, err := extractBinary(duplicate, "termada"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate binary member error = %v", err)
	}

	empty := makeArchive(t, &tar.Header{Name: "termada", Mode: 0o755, Typeflag: tar.TypeReg}, nil)
	if _, err := extractBinary(empty, "termada"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty binary member error = %v", err)
	}
}

func TestReadLimited(t *testing.T) {
	if got, err := readLimited(strings.NewReader("1234"), 4); err != nil || string(got) != "1234" {
		t.Fatalf("exact limit = %q, %v", got, err)
	}
	if _, err := readLimited(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("oversized response accepted")
	}
}
