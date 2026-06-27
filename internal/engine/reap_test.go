package engine

import (
	"os/exec"
	"testing"
	"time"
)

// With max_job_runtime_ms set, a job that overruns it is SIGKILLed by the reaper
// so it can't pin its session and the foreground quota forever.
func TestReapOnceKillsOverrunningJob(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	cfg := DefaultConfig()
	cfg.MaxJobRuntimeMS = 150
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)

	job, err := m.Start("agent", "", []string{"sleep", "30"}, ModeBackground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Wait until it is genuinely running (startedAt set), then exceed the cap.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := job.info().Status; s == StatusRunning || s == StatusBackgrounded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)

	if n := m.ReapOnce(); n != 1 {
		t.Fatalf("ReapOnce reaped %d, want 1", n)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("reaped job did not terminate; status=%s", job.info().Status)
	}
}

// With no cap configured (the default), the reaper never touches anything — a
// long-lived dev server must not be killed.
func TestReapOnceDisabledByDefault(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	m := NewManager(DefaultConfig()) // MaxJobRuntimeMS == 0
	t.Cleanup(m.Shutdown)

	if _, err := m.Start("agent", "", []string{"sleep", "5"}, ModeBackground); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := m.ReapOnce(); n != 0 {
		t.Fatalf("ReapOnce reaped %d with no cap, want 0", n)
	}
}
