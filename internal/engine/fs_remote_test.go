package engine

import (
	"os/exec"
	"path/filepath"
	"testing"
)

type fakeRemoteFiles struct {
	reads, writes int
	content       string
}

func (f *fakeRemoteFiles) ReadFile(target, path string, maxBytes int) ([]byte, int64, bool, error) {
	f.reads++
	return []byte(f.content), int64(len(f.content)), false, nil
}

func (f *fakeRemoteFiles) WriteFile(target, path, content, mode string) (int, error) {
	f.writes++
	return len(content), nil
}

// FileReadAt/FileWriteAt route a remote session to the RemoteFileOps backend and
// a local session to the local filesystem.
func TestFileOpsRouteBySessionTarget(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetRemoteDialer(func(target string, cols, rows int) (ShellConn, error) {
		return startShell(cols, rows, SpawnConfig{})
	})
	frf := &fakeRemoteFiles{content: "remote-data"}
	m.SetRemoteFileOps(frf)

	rem, err := m.CreateSession("a", "prod", "shell")
	if err != nil {
		t.Fatalf("remote session: %v", err)
	}
	res, err := m.FileReadAt(rem.ID, "/etc/thing", 0)
	if err != nil || res.Content != "remote-data" || frf.reads != 1 {
		t.Fatalf("remote read not routed: res=%+v reads=%d err=%v", res, frf.reads, err)
	}
	if wr, err := m.FileWriteAt(rem.ID, "/etc/thing", "hi", ""); err != nil || !wr.OK || frf.writes != 1 {
		t.Fatalf("remote write not routed: wr=%+v writes=%d err=%v", wr, frf.writes, err)
	}

	// A local session must still hit the local filesystem (not the remote backend).
	loc, err := m.CreateSession("a", "local", "shell")
	if err != nil {
		t.Fatalf("local session: %v", err)
	}
	p := filepath.Join(t.TempDir(), "f.txt")
	if _, err := m.FileWriteAt(loc.ID, p, "localdata", ""); err != nil {
		t.Fatalf("local write: %v", err)
	}
	r2, err := m.FileReadAt(loc.ID, p, 0)
	if err != nil || r2.Content != "localdata" {
		t.Fatalf("local read: %+v err=%v", r2, err)
	}
	if frf.reads != 1 || frf.writes != 1 {
		t.Fatalf("local op leaked to remote backend: reads=%d writes=%d", frf.reads, frf.writes)
	}
}
