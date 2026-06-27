package engine

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// PollWait long-polls: with a wait budget it blocks until output appears rather
// than returning empty, so the agent doesn't need a manual poll-sleep loop.
func TestPollWaitBlocksForOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)

	job, err := m.Start("agent", "", []string{"bash", "-lc", "sleep 0.4; echo HELLO"}, ModeBackground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	start := time.Now()
	res, err := m.PollWait(job.ID, "", 3000)
	if err != nil {
		t.Fatalf("pollwait: %v", err)
	}
	if !strings.Contains(res.StdoutChunk, "HELLO") {
		t.Fatalf("long-poll returned no output: %q (status %s)", res.StdoutChunk, res.Status)
	}
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Fatalf("long-poll returned too fast (%v) — it should have blocked for output", waited)
	}
}

// With waitMS=0 PollWait is the old non-blocking poll (returns immediately).
func TestPollWaitNonBlockingWhenZero(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)

	job, err := m.Start("agent", "", []string{"bash", "-lc", "sleep 2"}, ModeBackground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	start := time.Now()
	if _, err := m.PollWait(job.ID, "", 0); err != nil {
		t.Fatalf("pollwait: %v", err)
	}
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("non-blocking poll blocked for %v", waited)
	}
}
