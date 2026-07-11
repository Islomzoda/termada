// Package selfupdate implements opt-in binary self-update (spec DI-3): fetch the
// latest GitHub release, download the asset for this platform, verify its
// SHA-256 against the published checksums (MANDATORY — an update with no
// verifiable checksum is refused), and atomically replace the running binary.
// When a release public key is configured (ReleasePublicKey), the checksums file
// must also carry a valid ed25519 signature, anchoring trust before any hash in
// it is trusted — so a compromised/spoofed release can't push a binary that runs
// as the daemon.
//
// The verify + atomic-replace primitives are unit-tested; the end-to-end Run
// needs a published release to exercise live. Windows update checks fail with an
// explicit manual-install instruction because a running .exe cannot be replaced
// atomically without a separate, durable updater helper.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	maxReleaseMetadataBytes = 2 << 20
	maxChecksumsBytes       = 2 << 20
	maxSignatureBytes       = 4 << 10
	maxReleaseArchiveBytes  = 128 << 20
	maxBinaryBytes          = 128 << 20
	maxExpandedArchiveBytes = 512 << 20
	maxArchiveEntries       = 4096
)

// ReleasePublicKey, when set to a base64-encoded 32-byte ed25519 public key,
// makes signature verification of the release checksums MANDATORY. Empty leaves
// the signature step off (checksum verification is still mandatory) — it is meant
// to be set via build ldflags / config once releases are signed.
var ReleasePublicKey = ""

// VerifySHA256 checks that data hashes to the expected hex digest.
func VerifySHA256(data []byte, wantHex string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, strings.TrimSpace(wantHex)) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, wantHex)
	}
	return nil
}

// SignEd25519 signs message with a base64-encoded ed25519 private key (the
// 64-byte form from ed25519.GenerateKey) and returns a base64 signature. Used by
// the release `sign-checksums` step; the matching VerifyEd25519 runs in-binary.
func SignEd25519(message []byte, privB64 string) (string, error) {
	priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privB64))
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("private key is %d bytes, want %d", len(priv), ed25519.PrivateKeySize)
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(priv), message)), nil
}

// VerifyEd25519 verifies a base64-encoded ed25519 signature over message using a
// base64-encoded 32-byte public key.
func VerifyEd25519(message []byte, sigB64, pubKeyB64 string) error {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubKeyB64))
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), message, sig) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

// Replace atomically swaps the file at target with new contents, preserving the
// executable bit. On Unix, the new file is written in the same directory and
// renamed over the target, so a running process keeps the old inode until it
// restarts. Run does not call this for a running Windows executable.
func Replace(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".termada-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func httpClient() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

// LatestTag returns the latest release tag for repo ("owner/name").
func LatestTag(repo string) (string, error) {
	rel, err := latestRelease(repo)
	if err != nil {
		return "", err
	}
	return rel.TagName, nil
}

func latestRelease(repo string) (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	resp, err := httpClient().Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases API returned %d", resp.StatusCode)
	}
	data, err := readLimited(resp.Body, maxReleaseMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("github releases metadata: %w", err)
	}
	var rel ghRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// Run checks for a newer release and, if found, updates the binary at the given
// path. It returns the tag it updated to (or the current one if up to date).
func Run(repo, current, targetPath string) (string, error) {
	rel, err := latestRelease(repo)
	if err != nil {
		return "", err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	if latest == "" {
		return "", fmt.Errorf("latest release has an empty version tag")
	}
	if _, err := parseSemver(latest); err != nil {
		return "", fmt.Errorf("latest release tag %q is not semantic versioning: %w", rel.TagName, err)
	}
	if currentVersion, err := parseSemver(strings.TrimPrefix(current, "v")); err == nil {
		latestVersion, _ := parseSemver(latest)
		if compareSemver(currentVersion, latestVersion) >= 0 {
			return current, nil
		}
	} else if latest == current {
		return current, nil
	}
	if !automaticReplaceSupported(runtime.GOOS) {
		return "", fmt.Errorf("automatic self-update is not supported on Windows because a running .exe cannot be atomically replaced; install %s manually", rel.TagName)
	}

	assetName := fmt.Sprintf("termada_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	var assetURL, sumsURL string
	for _, a := range rel.Assets {
		switch {
		case a.Name == assetName:
			assetURL = a.BrowserDownloadURL
		case a.Name == "checksums.txt":
			sumsURL = a.BrowserDownloadURL
		}
	}
	if assetURL == "" {
		return "", fmt.Errorf("no release asset %s for this platform", assetName)
	}

	// Checksum verification is mandatory: refuse to install a binary we can't
	// verify rather than trusting a possibly-tampered download.
	if sumsURL == "" {
		return "", fmt.Errorf("release %s has no checksums.txt; refusing to install an unverified binary", rel.TagName)
	}
	sums, err := download(sumsURL, maxChecksumsBytes)
	if err != nil {
		return "", err
	}
	// With a release key configured, the checksums file must carry a valid
	// ed25519 signature — anchor trust here before trusting any hash inside it.
	if ReleasePublicKey != "" {
		var sigURL string
		for _, a := range rel.Assets {
			if a.Name == "checksums.txt.sig" {
				sigURL = a.BrowserDownloadURL
			}
		}
		if sigURL == "" {
			return "", fmt.Errorf("release %s has no checksums.txt.sig but a release key is configured; refusing", rel.TagName)
		}
		sig, err := download(sigURL, maxSignatureBytes)
		if err != nil {
			return "", err
		}
		if err := VerifyEd25519(sums, string(sig), ReleasePublicKey); err != nil {
			return "", fmt.Errorf("checksums signature: %w", err)
		}
	}
	want, ok := checksumFor(string(sums), assetName)
	if !ok {
		return "", fmt.Errorf("no checksum for %s", assetName)
	}
	archive, err := download(assetURL, maxReleaseArchiveBytes)
	if err != nil {
		return "", err
	}
	if err := VerifySHA256(archive, want); err != nil {
		return "", err
	}

	bin, err := extractBinary(archive, platformBinaryName(runtime.GOOS))
	if err != nil {
		return "", err
	}
	if err := Replace(targetPath, bin); err != nil {
		return "", err
	}
	return rel.TagName, nil
}

type semanticVersion struct {
	major, minor, patch int
	pre                 []string
}

func parseSemver(value string) (semanticVersion, error) {
	var build string
	var hasBuild bool
	value, build, hasBuild = strings.Cut(value, "+")
	if hasBuild {
		if build == "" {
			return semanticVersion{}, fmt.Errorf("empty build metadata")
		}
		for _, identifier := range strings.Split(build, ".") {
			if identifier == "" || !semverIdentifier(identifier) {
				return semanticVersion{}, fmt.Errorf("invalid build identifier %q", identifier)
			}
		}
	}
	core, pre, hasPre := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, fmt.Errorf("version core must have major.minor.patch")
	}
	numbers := [3]int{}
	for i, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return semanticVersion{}, fmt.Errorf("invalid numeric component %q", part)
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return semanticVersion{}, fmt.Errorf("invalid numeric component %q", part)
		}
		numbers[i] = n
	}
	v := semanticVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}
	if hasPre {
		if pre == "" {
			return semanticVersion{}, fmt.Errorf("empty prerelease")
		}
		v.pre = strings.Split(pre, ".")
		for _, identifier := range v.pre {
			if identifier == "" || !semverIdentifier(identifier) {
				return semanticVersion{}, fmt.Errorf("invalid prerelease identifier %q", identifier)
			}
			if numericIdentifier(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return semanticVersion{}, fmt.Errorf("numeric prerelease identifier has a leading zero")
			}
		}
	}
	return v, nil
}

func semverIdentifier(value string) bool {
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func numericIdentifier(value string) bool {
	return strings.Trim(value, "0123456789") == ""
}

func compareSemver(a, b semanticVersion) int {
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(a.pre) == 0 || len(b.pre) == 0 {
		switch {
		case len(a.pre) == 0 && len(b.pre) > 0:
			return 1
		case len(a.pre) > 0 && len(b.pre) == 0:
			return -1
		default:
			return 0
		}
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if a.pre[i] == b.pre[i] {
			continue
		}
		aNumeric, bNumeric := numericIdentifier(a.pre[i]), numericIdentifier(b.pre[i])
		switch {
		case aNumeric && bNumeric:
			if len(a.pre[i]) < len(b.pre[i]) || len(a.pre[i]) == len(b.pre[i]) && a.pre[i] < b.pre[i] {
				return -1
			}
			return 1
		case aNumeric:
			return -1
		case bNumeric:
			return 1
		case a.pre[i] < b.pre[i]:
			return -1
		default:
			return 1
		}
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}

func download(url string, maxBytes int64) ([]byte, error) {
	resp, err := httpClient().Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("download %s is %d bytes, exceeds %d byte limit", url, resp.ContentLength, maxBytes)
	}
	data, err := readLimited(resp.Body, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	return data, nil
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds %d byte limit", maxBytes)
	}
	return data, nil
}

// checksumFor finds the hash for name in a `<hash>  <name>` checksums file.
func checksumFor(sums, name string) (string, bool) {
	var found string
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		file := strings.TrimPrefix(fields[1], "*")
		if file != name {
			continue
		}
		if len(fields) != 2 || !validSHA256(fields[0]) || found != "" {
			// Malformed or duplicate records for the requested asset are ambiguous.
			return "", false
		}
		found = fields[0]
	}
	return found, found != ""
}

func validSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func platformBinaryName(goos string) string {
	if goos == "windows" {
		return "termada.exe"
	}
	return "termada"
}

func automaticReplaceSupported(goos string) bool { return goos != "windows" }

// extractBinary pulls a named file out of a .tar.gz archive.
func extractBinary(targz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(targz))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var expanded int64
	entries := 0
	var binary []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		entries++
		if entries > maxArchiveEntries {
			return nil, fmt.Errorf("archive contains more than %d entries", maxArchiveEntries)
		}
		if hdr.Size < 0 || hdr.Size > maxExpandedArchiveBytes-expanded {
			return nil, fmt.Errorf("expanded archive exceeds %d byte limit", int64(maxExpandedArchiveBytes))
		}
		expanded += hdr.Size
		if hdr.Name == name {
			if binary != nil {
				return nil, fmt.Errorf("archive contains duplicate binary %q", name)
			}
			if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
				return nil, fmt.Errorf("binary %q is not a regular archive entry", name)
			}
			if hdr.Size > maxBinaryBytes {
				return nil, fmt.Errorf("binary %q exceeds %d byte limit", name, int64(maxBinaryBytes))
			}
			bin, err := readLimited(tr, maxBinaryBytes)
			if err != nil {
				return nil, fmt.Errorf("extract binary %q: %w", name, err)
			}
			if len(bin) == 0 {
				return nil, fmt.Errorf("binary %q is empty", name)
			}
			binary = bin
		}
	}
	if binary != nil {
		return binary, nil
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}
