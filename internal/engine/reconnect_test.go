package engine

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// reconnShell is a test transport that wraps a real local bash PTY (so the
// marker protocol genuinely runs) and supports a simulated connection drop plus
// a Reconnect that swaps in a fresh shell — exactly the contract sshShell offers
// for remote sessions, but driveable from a test without SSH or tmux.
type reconnShell struct {
	mu         sync.Mutex
	inner      ShellConn
	reconnects int
}

func newReconnShell() (*reconnShell, error) {
	in, err := startShell(200, 50, SpawnConfig{})
	if err != nil {
		return nil, err
	}
	return &reconnShell{inner: in}, nil
}

func (s *reconnShell) get() ShellConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner
}

func (s *reconnShell) Read(p []byte) (int, error)  { return s.get().Read(p) }
func (s *reconnShell) Write(p []byte) (int, error) { return s.get().Write(p) }
func (s *reconnShell) Close() error                { return s.get().Close() }
func (s *reconnShell) Signal(n string) error       { return s.get().Signal(n) }

func (s *reconnShell) Reconnect() error {
	in, err := startShell(200, 50, SpawnConfig{})
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.inner
	s.inner = in
	s.reconnects++
	s.mu.Unlock()
	_ = old.Close()
	return nil
}

// drop simulates a lost connection: closing the inner shell makes Read error,
// which the session reader treats as a drop and tries to recover from.
func (s *reconnShell) drop() { _ = s.get().Close() }

func (s *reconnShell) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconnects
}

func TestSessionReconnectsAfterDrop(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)

	var rs *reconnShell
	m.SetRemoteDialer(func(target string, cols, rows int) (ShellConn, error) {
		r, err := newReconnShell()
		if err == nil {
			rs = r
		}
		return r, err
	})
	sess, err := m.CreateSession("agent", "remote", "shell")
	if err != nil {
		t.Fatalf("create remote session: %v", err)
	}

	// works before the drop
	res, err := m.Run("agent", sess.ID, []string{"echo", "before"}, "", 3000)
	if err != nil || !strings.Contains(res.Stdout, "before") {
		t.Fatalf("before drop: err=%v out=%q", err, res.Stdout)
	}

	// simulate a dropped connection; the session should transparently reconnect
	rs.drop()

	// poll until the session recovers, then a fresh command must succeed
	deadline := time.Now().Add(15 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		if r, e := m.Run("agent", sess.ID, []string{"echo", "after"}, "", 3000); e == nil && strings.Contains(r.Stdout, "after") {
			recovered = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !recovered {
		t.Fatalf("session did not recover after drop (reconnects=%d)", rs.count())
	}
	if rs.count() == 0 {
		t.Fatalf("expected at least one reconnect")
	}
}
