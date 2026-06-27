package engine

import (
	"os/exec"
	"testing"

	"github.com/termada/termada/internal/errs"
)

// file_read/file_write must never silently touch the local FS when the agent is
// in a remote session. SessionTarget reports the host, and EnsureLocalFileOp
// turns a remote session into a loud, actionable refusal. A local bash PTY
// stands in for the SSH transport so the marker protocol genuinely runs.
func TestEnsureLocalFileOpGuardsRemoteSessions(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetRemoteDialer(func(target string, cols, rows int) (ShellConn, error) {
		return startShell(cols, rows, SpawnConfig{})
	})

	remote, err := m.CreateSession("agent", "prod-box", "shell")
	if err != nil {
		t.Fatalf("create remote session: %v", err)
	}
	if target, ok := m.SessionTarget(remote.ID); !ok || target != "prod-box" {
		t.Fatalf("SessionTarget(remote) = %q,%v; want prod-box,true", target, ok)
	}
	err = m.EnsureLocalFileOp(remote.ID)
	if err == nil {
		t.Fatal("EnsureLocalFileOp on a remote session should refuse, got nil")
	}
	if e, ok := err.(*errs.Error); !ok || e.Code != errs.NotSupported {
		t.Fatalf("want not_supported error, got %v", err)
	}

	local, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create local session: %v", err)
	}
	if target, ok := m.SessionTarget(local.ID); !ok || target != "local" {
		t.Fatalf("SessionTarget(local) = %q,%v; want local,true", target, ok)
	}
	if err := m.EnsureLocalFileOp(local.ID); err != nil {
		t.Fatalf("local session file op should pass: %v", err)
	}

	// The empty/default session id is reported not-found and treated as local.
	if _, ok := m.SessionTarget(""); ok {
		t.Fatal(`SessionTarget("") should be not-found`)
	}
	if err := m.EnsureLocalFileOp(""); err != nil {
		t.Fatalf("default session file op should pass: %v", err)
	}
}
