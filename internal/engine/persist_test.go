package engine

import (
	"path/filepath"
	"testing"
	"time"
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
