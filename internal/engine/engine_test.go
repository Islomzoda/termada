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
	res, err := m.Poll("agent", jobID, "")
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
	job, err := m.Start("agent", "", []string{"bash", "-c", "printf 'Value: '; read -r x; sleep 0.3; echo got=$x"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	sessionView, err := m.SessionTail(job.SessionID, "")
	if err != nil {
		t.Fatalf("session tail: %v", err)
	}
	if !sessionView.AwaitingInput || sessionView.Prompt != "Value:" {
		t.Fatalf("session prompt = %+v, want awaiting Value:", sessionView)
	}
	if err := m.Write("agent", job.ID, "pizza", true, false, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	if info := job.Snapshot(); info.AwaitingInput || info.Status == StatusAwaitingInput {
		t.Fatalf("answered prompt remained active: %+v", info)
	}
	waitDone(t, job, 5*time.Second)
	res := poll(t, m, job.ID)
	if !strings.Contains(res.StdoutChunk, "got=pizza") {
		t.Fatalf("output = %q, want got=pizza", res.StdoutChunk)
	}
}

func TestSuccessfulJobSecretIsReleasedAfterDone(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"bash", "-c", "printf 'Value: '; read -r value; printf 'seen=%s\\n' \"$value\"; sleep 1"}, ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}

	const secret = "job-lifetime-secret-value"
	if err := m.Write("agent", job.ID, secret, true, true, false); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if got := m.Redactor().Redact(secret); got == secret {
		t.Fatal("successful job secret was released while job was still running")
	}
	select {
	case <-job.Done():
		t.Fatal("job completed before running-lifetime assertion")
	default:
	}

	deadline = time.Now().Add(3 * time.Second)
	var runningOutput string
	for time.Now().Before(deadline) {
		res := poll(t, m, job.ID)
		runningOutput = res.StdoutChunk
		if strings.Contains(runningOutput, "REDACTED") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(runningOutput, secret) || !strings.Contains(runningOutput, "REDACTED") {
		t.Fatalf("running job output was not redacted: %q", runningOutput)
	}

	waitDone(t, job, 5*time.Second)
	if got := m.Redactor().Redact(secret); got != secret {
		t.Fatalf("completed job retained secret literal: %q", got)
	}
	if finalOutput := poll(t, m, job.ID).StdoutChunk; strings.Contains(finalOutput, secret) {
		t.Fatalf("completed job output leaked released secret: %q", finalOutput)
	}
}

func TestSuccessfulJobSecretDoesNotRemoveExistingPinnedLiteral(t *testing.T) {
	m := newTestManager(t)
	const secret = "existing-pinned-job-secret"
	if err := m.Redactor().AddLiteral(secret); err != nil {
		t.Fatalf("pin secret: %v", err)
	}
	job, err := m.Start("agent", "", []string{"bash", "-c", "printf 'Value: '; read -r value"}, ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	if err := m.Write("agent", job.ID, secret, true, true, false); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	waitDone(t, job, 5*time.Second)
	if got := m.Redactor().Redact(secret); got == secret {
		t.Fatal("job completion removed a literal pinned by AddLiteral")
	}
}

func TestPromptMetadataIsRedacted(t *testing.T) {
	m := newTestManager(t)
	const secret = "prompt-secret"
	if err := m.Redactor().AddLiteral(secret); err != nil {
		t.Fatal(err)
	}
	job, err := m.Start("agent", "", []string{"bash", "-c", "printf 'Password prompt-secret: '; read -r x"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	info := job.Snapshot()
	if !info.AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	masked := strings.Contains(info.Prompt, "REDACTED") || strings.Contains(info.Prompt, "***")
	if strings.Contains(info.Prompt, secret) || !masked {
		t.Fatalf("prompt was not redacted: %q", info.Prompt)
	}
	if err := m.Write("agent", job.ID, "done", true, true, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitDone(t, job, 5*time.Second)
}

func TestPromptMetadataOnlyShowsCurrentQuestion(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"bash", "-c", "printf 'First? '; read -r first; printf '   Second? '; read -r second; printf '\\rPath: invalid, retry? '; read -r third; printf '\\nFirst? '; read -r fourth; echo done"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitPrompt := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if info := job.Snapshot(); info.AwaitingInput && info.Prompt == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("prompt = %q, want %q", job.Snapshot().Prompt, want)
	}
	waitPrompt("First?")
	if err := m.Write("agent", job.ID, "one", true, false, false); err != nil {
		t.Fatalf("answer first prompt: %v", err)
	}
	waitPrompt("Second?")
	if err := m.Write("agent", job.ID, "two", true, false, false); err != nil {
		t.Fatalf("answer second prompt: %v", err)
	}
	waitPrompt("Path: invalid, retry?")
	if err := m.Write("agent", job.ID, "three", true, false, false); err != nil {
		t.Fatalf("answer third prompt: %v", err)
	}
	waitPrompt("First?")
	if err := m.Write("agent", job.ID, "four", true, false, false); err != nil {
		t.Fatalf("answer fourth prompt: %v", err)
	}
	waitDone(t, job, 5*time.Second)
}

func TestSessionTailFlushesCompletedUnterminatedOutput(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"printf", "unterminated"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, job, 5*time.Second)
	res, err := m.SessionTail(job.SessionID, "")
	if err != nil {
		t.Fatalf("session tail: %v", err)
	}
	if res.Chunk != "unterminated" {
		t.Fatalf("session output = %q, want unterminated", res.Chunk)
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
		if err := m.Kill("agent", job.ID); err == nil {
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
	failed, err := m.Start("agent", sess.ID, []string{"echo", "x"}, "")
	if err == nil {
		t.Fatalf("expected session_busy error, got nil")
	}
	e, ok := err.(*errs.Error)
	if !ok || e.Code != errs.SessionBusy {
		t.Fatalf("error = %v, want session_busy", err)
	}
	if failed == nil || failed.Snapshot().Status != StatusFailed {
		t.Fatalf("busy start job = %+v, want terminal failed job", failed)
	}
	select {
	case <-failed.Done():
	default:
		t.Fatal("busy start left a non-terminal phantom job")
	}
	if active := m.ListJobs("agent", "active"); len(active) != 1 || active[0].JobID != j1.ID {
		t.Fatalf("active jobs after busy start = %+v, want only %s", active, j1.ID)
	}
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if m.Kill("agent", j1.ID) == nil {
			break
		}
	}
	waitDone(t, j1, 5*time.Second)
}

func TestLeadingAssignmentWordIsAlwaysQuoted(t *testing.T) {
	got := quoteArgv([]string{"X=1", "printf", "PWNED"})
	if got != "'X=1' 'printf' 'PWNED'" {
		t.Fatalf("quoteArgv = %q", got)
	}
}
