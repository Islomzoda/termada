package termada_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeCurl = `#!/bin/sh
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift 2 ;;
    --max-filesize) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
printf '%s\n' "$url" >> "$REQUEST_LOG"
case "$url" in
  */checksums.txt) src="$FIXTURE_DIR/checksums.txt" ;;
  */termada_linux_amd64.tar.gz) src="$FIXTURE_DIR/termada_linux_amd64.tar.gz" ;;
  *) exit 22 ;;
esac
[ -f "$src" ] || exit 22
cp "$src" "$out"
`

const fakeUname = `#!/bin/sh
case "$1" in
  -s) echo Linux ;;
  -m) echo x86_64 ;;
  *) exit 1 ;;
esac
`

type installFixture struct {
	root       string
	fixtures   string
	binDir     string
	fakeBin    string
	requestLog string
	archive    string
}

func newInstallFixture(t *testing.T) installFixture {
	t.Helper()
	root := t.TempDir()
	f := installFixture{
		root: root, fixtures: filepath.Join(root, "fixtures"),
		binDir: filepath.Join(root, "bin"), fakeBin: filepath.Join(root, "fake-bin"),
		requestLog: filepath.Join(root, "requests.log"),
	}
	if err := os.MkdirAll(f.fixtures, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(f.fakeBin, "curl"), fakeCurl)
	writeExecutable(t, filepath.Join(f.fakeBin, "uname"), fakeUname)
	f.archive = filepath.Join(f.fixtures, "termada_linux_amd64.tar.gz")
	writeInstallerArchive(t, f.archive, []byte("#!/bin/sh\nprintf 'fixture version\\n'\n"))
	return f
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeInstallerArchive(t *testing.T, path string, binary []byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "termada", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f installFixture) writeChecksum(t *testing.T, sum string) {
	t.Helper()
	line := fmt.Sprintf("%s  termada_linux_amd64.tar.gz\n", sum)
	if err := os.WriteFile(filepath.Join(f.fixtures, "checksums.txt"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f installFixture) run(t *testing.T) ([]byte, error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join(wd, "install.sh"))
	cmd.Dir = wd
	cmd.Env = append(os.Environ(),
		"TERMADA_VERSION=vtest",
		"TERMADA_BIN_DIR="+f.binDir,
		"FIXTURE_DIR="+f.fixtures,
		"REQUEST_LOG="+f.requestLog,
		"PATH="+f.fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+f.root,
	)
	return cmd.CombinedOutput()
}

func TestInstallerVerifiesBeforeInstall(t *testing.T) {
	t.Run("verified archive installs", func(t *testing.T) {
		f := newInstallFixture(t)
		archive, err := os.ReadFile(f.archive)
		if err != nil {
			t.Fatal(err)
		}
		f.writeChecksum(t, fmt.Sprintf("%x", sha256.Sum256(archive)))
		out, err := f.run(t)
		if err != nil {
			t.Fatalf("install failed: %v\n%s", err, out)
		}
		installed, err := os.ReadFile(filepath.Join(f.binDir, "termada"))
		if err != nil || !bytes.Contains(installed, []byte("fixture version")) {
			t.Fatalf("installed binary = %q, err=%v", installed, err)
		}
	})

	t.Run("missing checksum never downloads archive", func(t *testing.T) {
		f := newInstallFixture(t)
		out, err := f.run(t)
		if err == nil || !strings.Contains(string(out), "refusing an unverified install") {
			t.Fatalf("missing checksum result: err=%v\n%s", err, out)
		}
		requests, _ := os.ReadFile(f.requestLog)
		if strings.Contains(string(requests), "termada_linux_amd64.tar.gz") {
			t.Fatalf("archive was downloaded before checksum validation: %s", requests)
		}
	})

	t.Run("checksum mismatch never invokes tar", func(t *testing.T) {
		f := newInstallFixture(t)
		f.writeChecksum(t, strings.Repeat("0", 64))
		tarLog := filepath.Join(f.root, "tar.log")
		writeExecutable(t, filepath.Join(f.fakeBin, "tar"), "#!/bin/sh\nprintf called > \"$TAR_LOG\"\nexit 99\n")
		t.Setenv("TAR_LOG", tarLog)
		out, err := f.run(t)
		if err == nil || !strings.Contains(string(out), "checksum mismatch") {
			t.Fatalf("checksum mismatch result: err=%v\n%s", err, out)
		}
		if _, err := os.Stat(tarLog); !os.IsNotExist(err) {
			t.Fatalf("tar ran before checksum verification: %v", err)
		}
	})

	t.Run("malformed checksum record is rejected", func(t *testing.T) {
		f := newInstallFixture(t)
		archive, err := os.ReadFile(f.archive)
		if err != nil {
			t.Fatal(err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(archive))
		malformed := sum + "  termada_linux_amd64.tar.gz trailing-field\n"
		if err := os.WriteFile(filepath.Join(f.fixtures, "checksums.txt"), []byte(malformed), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := f.run(t)
		if err == nil || !strings.Contains(string(out), "invalid SHA-256") {
			t.Fatalf("malformed checksum result: err=%v\n%s", err, out)
		}
	})
}
