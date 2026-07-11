package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/policy"
)

func TestPersistAndRecoverOrphaned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")

	// First run: start a long job; it is persisted as running.
	m1 := NewManager(DefaultConfig())
	if err := m1.EnablePersistence(path); err != nil {
		t.Fatalf("enable persistence: %v", err)
	}
	job, err := m1.Start("agent", "", []string{"sleep", "30"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Tear down deterministically so the async persist-on-finish does not race
	// the temp-dir cleanup: kill the job, wait for it, then let persist settle.
	t.Cleanup(func() {
		_ = m1.Kill("agent", job.ID)
		<-job.Done()
		m1.Shutdown()
		time.Sleep(150 * time.Millisecond)
	})

	// Second run (simulated crash — m1 was never gracefully shut down): recover.
	m2 := NewManager(DefaultConfig())
	if err := m2.EnablePersistence(path); err != nil {
		t.Fatalf("recover: %v", err)
	}
	var found *Info
	for _, in := range m2.ListJobs("agent", "all") {
		if in.JobID == job.ID {
			cp := in
			found = &cp
		}
	}
	if found == nil {
		t.Fatalf("recovered registry missing job %s", job.ID)
	}
	if found.Status != StatusOrphaned {
		t.Fatalf("recovered job status = %s, want orphaned", found.Status)
	}
}

func TestPersistenceWriteFailureIsObservableAndRecovers(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "registry-dir")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "registry.json")
	m := NewManager(DefaultConfig())
	b := bus.New(16)
	m.SetBus(b)
	if err := m.EnablePersistence(path); err != nil {
		t.Fatalf("enable persistence: %v", err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.persist(); err == nil {
		t.Fatal("persist succeeded through a non-directory parent")
	}
	status := m.PersistenceStatus()
	if !status.Enabled || status.Healthy || status.Error == "" {
		t.Fatalf("failed persistence status = %+v", status)
	}
	recent := b.Recent(16)
	found := false
	for _, event := range recent {
		if event.Type == bus.EvPersistenceError && event.Data["operation"] == "write" {
			found = true
		}
	}
	if !found {
		t.Fatalf("persistence failure event missing: %+v", recent)
	}

	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.persist(); err != nil {
		t.Fatalf("persist after repair: %v", err)
	}
	status = m.PersistenceStatus()
	if !status.Enabled || !status.Healthy || status.Error != "" {
		t.Fatalf("recovered persistence status = %+v", status)
	}
}

func TestPendingConfirmationFailsWhenRegistryCannotPersist(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "registry-dir")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	m := NewManager(DefaultConfig())
	if err := m.EnablePersistence(filepath.Join(parent, "registry.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	session := &Session{ID: "session", Owner: "agent", cfg: SessionConfig{OutputRetentionBytes: 1024}, redactor: m.redactor}
	job := newConfirmJob(session, []string{"dangerous"}, ModeForeground)
	pending := &pendingConfirm{ID: "confirm", Job: job, Owner: "agent", Sess: session}
	err := m.registerPending(pending)
	structured, ok := err.(*errs.Error)
	if !ok || structured.Code != errs.Internal {
		t.Fatalf("registerPending error = %v, want internal persistence error", err)
	}
	m.mu.Lock()
	_, hasJob := m.jobs[job.ID]
	_, hasPending := m.pending[pending.ID]
	m.mu.Unlock()
	if hasJob || hasPending {
		t.Fatalf("failed pending registration remained visible: job=%v pending=%v", hasJob, hasPending)
	}
}

func TestRecoverKeepsTerminalStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")

	m1 := NewManager(DefaultConfig())
	if err := m1.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	job, err := m1.Start("agent", "", []string{"echo", "hi"}, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}
	// give the watch goroutine a moment to persist the terminal state
	time.Sleep(100 * time.Millisecond)
	m1.Shutdown()

	m2 := NewManager(DefaultConfig())
	if err := m2.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	for _, in := range m2.ListJobs("agent", "all") {
		if in.JobID == job.ID {
			if in.Status != StatusExited {
				t.Fatalf("terminal job recovered as %s, want exited (graceful, not orphaned)", in.Status)
			}
			return
		}
	}
	t.Fatalf("recovered registry missing job %s", job.ID)
}

func TestResolvedConfirmationPersistsTerminalState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout bool
		reason  string
	}{
		{name: "denied", reason: "denied"},
		{name: "expired", timeout: true, reason: "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "registry.json")
			cfg := DefaultConfig()
			if tc.timeout {
				cfg.ConfirmTimeoutMS = 25
			}
			m1 := NewManager(cfg)
			t.Cleanup(m1.Shutdown)
			if err := m1.EnablePersistence(path); err != nil {
				t.Fatalf("enable persistence: %v", err)
			}
			m1.SetPolicy(policy.NewEngine(map[string]policy.Policy{
				"p": {Confirm: []string{"echo*"}},
			}), map[string]string{"agent": "p"})
			job, err := m1.Start("agent", "", []string{"echo", "never-runs"}, ModeForeground)
			if err != nil {
				t.Fatalf("start confirmation: %v", err)
			}
			if !tc.timeout {
				if err := m1.Deny(job.Snapshot().ConfirmationID, "tester"); err != nil {
					t.Fatalf("deny: %v", err)
				}
			}
			waitDone(t, job, 2*time.Second)
			if tc.timeout {
				// The timer resolves on its own goroutine. Job completion is published by
				// finalize just before that goroutine performs the synchronous registry
				// write, so wait for the durable state rather than racing the last write.
				deadline := time.Now().Add(2 * time.Second)
				for {
					data, _ := os.ReadFile(path)
					if strings.Contains(string(data), `"status": "failed"`) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("timed-out confirmation was not persisted: %s", data)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}

			m2 := NewManager(DefaultConfig())
			t.Cleanup(m2.Shutdown)
			if err := m2.EnablePersistence(path); err != nil {
				t.Fatalf("recover: %v", err)
			}
			for _, recovered := range m2.ListJobs("agent", "all") {
				if recovered.JobID != job.ID {
					continue
				}
				if recovered.Status != StatusFailed || !strings.Contains(recovered.Reason, tc.reason) {
					t.Fatalf("recovered confirmation = %+v, want failed reason containing %q", recovered, tc.reason)
				}
				return
			}
			t.Fatalf("recovered registry missing confirmation job %s", job.ID)
		})
	}
}
