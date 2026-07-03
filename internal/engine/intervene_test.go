package engine

import (
	"strings"
	"testing"
	"time"
)

func TestHoldInputBlocksAgentAllowsHuman(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"bash", "-c", "read -r x; echo got=$x"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	yes := true
	if err := m.Hold(job.ID, &yes, nil); err != nil {
		t.Fatalf("hold: %v", err)
	}
	// Agent input must be rejected while a human holds the terminal.
	if err := m.Write("agent", job.ID, "agent-typed", true, false, false); err == nil {
		t.Fatal("agent write should be blocked while input is held")
	}
	// Human input goes through.
	time.Sleep(150 * time.Millisecond)
	if err := m.Write("agent", job.ID, "human-typed", true, false, true); err != nil {
		t.Fatalf("human write: %v", err)
	}
	waitDone(t, job, 5*time.Second)
	res := poll(t, m, job.ID)
	if !strings.Contains(res.StdoutChunk, "got=human-typed") {
		t.Fatalf("output = %q, want got=human-typed", res.StdoutChunk)
	}
}

func TestSessionTerminalCapturesAcrossJobs(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, word := range []string{"alpha", "beta"} {
		j, err := m.Start("agent", sess.ID, []string{"echo", word}, "")
		if err != nil {
			t.Fatalf("start %s: %v", word, err)
		}
		waitDone(t, j, 5*time.Second)
	}
	res, err := m.SessionTail(sess.ID, "")
	if err != nil {
		t.Fatalf("session tail: %v", err)
	}
	// One continuous terminal for the session contains BOTH commands' output.
	if !strings.Contains(res.Chunk, "alpha") || !strings.Contains(res.Chunk, "beta") {
		t.Fatalf("session terminal = %q, want both alpha and beta", res.Chunk)
	}
}

func TestHoldOutputPausesAgentOnly(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"sh", "-c", "echo first; sleep 0.3; echo second"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	yes := true
	if err := m.Hold(job.ID, nil, &yes); err != nil {
		t.Fatalf("hold: %v", err)
	}
	// Agent poll sees no new bytes and the held flag, even as output is produced.
	res, err := m.Poll("agent", job.ID, "")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !res.OutputHeld || res.StdoutChunk != "" {
		t.Fatalf("agent poll while held = %+v, want held with no chunk", res)
	}
	// The human stream (human=true) still sees output.
	waitDone(t, job, 5*time.Second)
	hres, _ := m.PollFor("agent", job.ID, "", true)
	if !strings.Contains(hres.StdoutChunk, "first") {
		t.Fatalf("human stream = %q, want it to contain output", hres.StdoutChunk)
	}
}
