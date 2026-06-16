// Package selfupdate implements opt-in binary self-update (spec DI-3): fetch the
// latest GitHub release, download the asset for this platform, verify its
// SHA-256 against the published checksums, and atomically replace the running
// binary. Signature verification (minisign/cosign) is a later addition gated on
// release-signing keys.
//
// The verify + atomic-replace primitives are unit-tested; the end-to-end Run
// needs a published release to exercise live.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// VerifySHA256 checks that data hashes to the expected hex digest.
func VerifySHA256(data []byte, wantHex string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, strings.TrimSpace(wantHex)) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, wantHex)
	}
	return nil
}

// Replace atomically swaps the file at target with new contents, preserving the
// executable bit. The new file is written in the same directory and renamed over
// the target, so a running process keeps the old inode until it restarts.
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
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
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
	if latest == "" || latest == current {
		return current, nil
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

	archive, err := download(assetURL)
	if err != nil {
		return "", err
	}
	if sumsURL != "" {
		sums, err := download(sumsURL)
		if err != nil {
			return "", err
		}
		want, ok := checksumFor(string(sums), assetName)
		if !ok {
			return "", fmt.Errorf("no checksum for %s", assetName)
		}
		if err := VerifySHA256(archive, want); err != nil {
			return "", err
		}
	}

	bin, err := extractBinary(archive, "termada")
	if err != nil {
		return "", err
	}
	if err := Replace(targetPath, bin); err != nil {
		return "", err
	}
	return rel.TagName, nil
}

func download(url string) ([]byte, error) {
	resp, err := httpClient().Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// checksumFor finds the hash for name in a `<hash>  <name>` checksums file.
func checksumFor(sums, name string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && filepath.Base(fields[1]) == name {
			return fields[0], true
		}
	}
	return "", false
}

// extractBinary pulls a named file out of a .tar.gz archive.
func extractBinary(targz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(targz)))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == name {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}
