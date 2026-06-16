package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/termada/termada/internal/errs"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	return m
}

func waitDone(t *testing.T, job *Job, d time.Duration) {
	t.Helper()
	select {
	case <-job.Done():
	case <-time.After(d):
		t.Fatalf("job %s did not finish within %s (status=%v)", job.ID, d, job.Snapshot().Status)
	}
}

func poll(t *testing.T, m *Manager, jobID string) *PollResult {
	t.Helper()
	res, err := m.Poll(jobID, "")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	return res
}

func TestExecEchoExitZero(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"echo", "hello world"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, job, 5*time.Second)
	res := poll(t, m, job.ID)
	if !strings.Contains(res.StdoutChunk, "hello world") {
		t.Fatalf("output = %q, want it to contain %q", res.StdoutChunk, "hello world")
	}
	if res.Status != StatusExited {
		t.Fatalf("status = %v, want exited", res.Status)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("exit code = %v, want 0", res.ExitCode)
	}
}

func TestExecFalseExitOne(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"false"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, job, 5*time.Second)
	res := poll(t, m, job.ID)
	if res.ExitCode == nil || *res.ExitCode != 1 {
		t.Fatalf("exit code = %v, want 1", res.ExitCode)
	}
}

func TestCwdPersists(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	j1, err := m.Start("agent", sess.ID, []string{"cd", "/tmp"}, "")
	if err != nil {
		t.Fatalf("cd: %v", err)
	}
	waitDone(t, j1, 5*time.Second)
	j2, err := m.Start("agent", sess.ID, []string{"pwd"}, "")
	if err != nil {
		t.Fatalf("pwd: %v", err)
	}
	waitDone(t, j2, 5*time.Second)
	res := poll(t, m, j2.ID)
	if !strings.Contains(res.StdoutChunk, "tmp") {
		t.Fatalf("pwd output = %q, want it to contain tmp", res.StdoutChunk)
	}
}

func TestEnvPersists(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	j1, err := m.Start("agent", sess.ID, []string{"export", "TERMADA_TEST=banana"}, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	waitDone(t, j1, 5*time.Second)
	j2, err := m.Start("agent", sess.ID, []string{"printenv", "TERMADA_TEST"}, "")
	if err != nil {
		t.Fatalf("printenv: %v", err)
	}
	waitDone(t, j2, 5*time.Second)
	res := poll(t, m, j2.ID)
	if !strings.Contains(res.StdoutChunk, "banana") {
		t.Fatalf("printenv output = %q, want banana", res.StdoutChunk)
	}
}

func TestExecWriteAnswersPrompt(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"bash", "-c", "read -r x; echo got=$x"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(150 * time.Millisecond) // let the inner read reach the prompt
	if err := m.Write(job.ID, "pizza", true, false, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitDone(t, job, 5*time.Second)
	res := poll(t, m, job.ID)
	if !strings.Contains(res.StdoutChunk, "got=pizza") {
		t.Fatalf("output = %q, want got=pizza", res.StdoutChunk)
	}
}

func TestKillSleep(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"sleep", "30"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// The shell needs a moment to fork sleep and make it the foreground group.
	var killed bool
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if err := m.Kill(job.ID); err == nil {
			killed = true
			break
		}
	}
	if !killed {
		t.Fatalf("could not deliver kill to job")
	}
	waitDone(t, job, 5*time.Second)
	if s := job.Snapshot().Status; s != StatusKilled {
		t.Fatalf("status = %v, want killed", s)
	}
}

func TestSessionBusy(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	j1, err := m.Start("agent", sess.ID, []string{"sleep", "2"}, "")
	if err != nil {
		t.Fatalf("start first: %v", err)
	}
	_, err = m.Start("agent", sess.ID, []string{"echo", "x"}, "")
	if err == nil {
		t.Fatalf("expected session_busy error, got nil")
	}
	e, ok := err.(*errs.Error)
	if !ok || e.Code != errs.SessionBusy {
		t.Fatalf("error = %v, want session_busy", err)
	}
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if m.Kill(j1.ID) == nil {
			break
		}
	}
	waitDone(t, j1, 5*time.Second)
}
