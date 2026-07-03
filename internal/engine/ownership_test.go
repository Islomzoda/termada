package engine

import (
	"testing"
	"time"

	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/policy"
)

// TestWriteRejectsNonCurrentJob guards against exec_write typing into a
// session's idle shell once the job it targets is no longer the session's
// current command: the write would otherwise execute as an unaudited,
// policy-ungated command in that shell instead of answering a (now gone)
// prompt.
func TestWriteRejectsNonCurrentJob(t *testing.T) {
	m := newTestManager(t)
	job, err := m.Start("agent", "", []string{"echo", "done"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, job, 5*time.Second)

	if err := m.Write("agent", job.ID, "rm -rf /whatever", true, false, false); err == nil {
		t.Fatal("write to a finished job should be rejected, not typed into the idle shell")
	}
}

// TestWriteRejectsJobDisplacedBySibling covers the same guard when a second
// job has since become the session's current job (not just "session idle").
func TestWriteRejectsJobDisplacedBySibling(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	j1, err := m.Start("agent", sess.ID, []string{"echo", "first"}, "")
	if err != nil {
		t.Fatalf("start j1: %v", err)
	}
	waitDone(t, j1, 5*time.Second)
	j2, err := m.Start("agent", sess.ID, []string{"sleep", "2"}, "")
	if err != nil {
		t.Fatalf("start j2: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 40 && m.Kill("agent", j2.ID) != nil; i++ {
			time.Sleep(50 * time.Millisecond)
		}
	})
	if err := m.Write("agent", j1.ID, "input for a job that no longer owns the shell", true, false, false); err == nil {
		t.Fatal("write to a displaced (no longer current) job should be rejected")
	}
}

// TestJobOwnershipIsolation is the core MA-2 regression: one agent cannot
// discover, poll, signal, kill, tail, write to, or close another agent's
// jobs/sessions through the scoped Manager API.
func TestJobOwnershipIsolation(t *testing.T) {
	m := newTestManager(t)
	victim, err := m.Start("victim", "", []string{"sleep", "5"}, "")
	if err != nil {
		t.Fatalf("start victim job: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 40 && m.Kill("victim", victim.ID) != nil; i++ {
			time.Sleep(50 * time.Millisecond)
		}
	})

	notFound := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected not_found, got nil")
		}
		e, ok := err.(*errs.Error)
		if !ok || e.Code != errs.NotFound {
			t.Fatalf("error = %v, want not_found", err)
		}
	}

	t.Run("Poll", func(t *testing.T) {
		_, err := m.Poll("attacker", victim.ID, "")
		notFound(t, err)
	})
	t.Run("Write", func(t *testing.T) {
		notFound(t, m.Write("attacker", victim.ID, "x", true, false, false))
	})
	t.Run("Signal", func(t *testing.T) {
		notFound(t, m.Signal("attacker", victim.ID, "SIGTERM"))
	})
	t.Run("Kill", func(t *testing.T) {
		notFound(t, m.Kill("attacker", victim.ID))
	})
	t.Run("Tail", func(t *testing.T) {
		_, err := m.Tail("attacker", victim.ID, "", false)
		notFound(t, err)
	})
	t.Run("ListJobs excludes other agents", func(t *testing.T) {
		for _, in := range m.ListJobs("attacker", "all") {
			if in.JobID == victim.ID {
				t.Fatalf("attacker's exec_list leaked victim's job %s", victim.ID)
			}
		}
		found := false
		for _, in := range m.ListJobs("victim", "all") {
			if in.JobID == victim.ID {
				found = true
			}
		}
		if !found {
			t.Fatal("victim's own exec_list should still show its job")
		}
	})
	t.Run("CloseSession", func(t *testing.T) {
		notFound(t, m.CloseSession("attacker", victim.SessionID))
	})

	// The owner-less (trusted/human) path must still see and control everything —
	// the dashboard/CLI are not agents and must not be scoped.
	t.Run("unscoped path still works", func(t *testing.T) {
		if _, err := m.Poll("", victim.ID, ""); err != nil {
			t.Fatalf("unscoped poll should succeed: %v", err)
		}
	})
}

// TestRunSurfacesAwaitingConfirmationImmediately guards the exec_run status
// contract: a confirm-gated command must come back as awaiting_confirmation
// right away, not get silently rewritten to "backgrounded" after burning the
// full wait budget (which would hide that a human needs to approve it).
func TestRunSurfacesAwaitingConfirmationImmediately(t *testing.T) {
	m := newTestManager(t)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"gate": {Confirm: []string{"danger*"}},
	}), map[string]string{"agent": "gate"})

	start := time.Now()
	res, err := m.Run("agent", "", []string{"danger-cmd"}, "", 0)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Run blocked for %v waiting out the timeout instead of returning immediately on a parked confirm", elapsed)
	}
	if res.Status != StatusAwaitingConfirmation {
		t.Fatalf("status = %s, want awaiting_confirmation", res.Status)
	}
	if res.ConfirmationID == "" {
		t.Fatal("expected a confirmation_id on the awaiting_confirmation result")
	}
}

// TestBackgroundJobsDontBlockForeground guards the MaxBackgroundJobs fix:
// long-running background jobs must not consume the (separate, smaller)
// foreground quota — otherwise a handful of dev servers left running would
// eventually block every other agent's exec_run/exec_start on the daemon.
func TestBackgroundJobsDontBlockForeground(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxForegroundJobs = 1
	cfg.MaxBackgroundJobs = 0 // unlimited background
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)

	// Start several background jobs — well past MaxForegroundJobs=1 — each in
	// its own session so they don't collide on session_busy.
	var bg []*Job
	for i := 0; i < 3; i++ {
		sess, err := m.CreateSession("agent", "local", "shell")
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		j, err := m.Start("agent", sess.ID, []string{"sleep", "5"}, ModeBackground)
		if err != nil {
			t.Fatalf("start background job %d: %v", i, err)
		}
		bg = append(bg, j)
	}
	t.Cleanup(func() {
		for _, j := range bg {
			for i := 0; i < 40 && m.Kill("agent", j.ID) != nil; i++ {
				time.Sleep(50 * time.Millisecond)
			}
		}
	})

	// A foreground command in a NEW session must still be able to run even
	// though 3 background jobs are live and MaxForegroundJobs is 1.
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	fg, err := m.Start("agent", sess.ID, []string{"echo", "still-works"}, "")
	if err != nil {
		t.Fatalf("foreground exec blocked by background jobs: %v", err)
	}
	waitDone(t, fg, 5*time.Second)
	if fg.Snapshot().Status != StatusExited {
		t.Fatalf("status = %v, want exited", fg.Snapshot().Status)
	}
}

// TestMaxBackgroundJobsGate exercises the separate background quota.
func TestMaxBackgroundJobsGate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxForegroundJobs = 0 // unlimited
	cfg.MaxBackgroundJobs = 1
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)

	sess1, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session 1: %v", err)
	}
	j1, err := m.Start("agent", sess1.ID, []string{"sleep", "5"}, ModeBackground)
	if err != nil {
		t.Fatalf("start first background job: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 40 && m.Kill("agent", j1.ID) != nil; i++ {
			time.Sleep(50 * time.Millisecond)
		}
	})

	sess2, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session 2: %v", err)
	}
	_, err = m.Start("agent", sess2.ID, []string{"sleep", "5"}, ModeBackground)
	if err == nil {
		t.Fatal("expected the second background job to be rejected by MaxBackgroundJobs=1")
	}
	e, ok := err.(*errs.Error)
	if !ok || e.Code != errs.ParallelismExceeded {
		t.Fatalf("error = %v, want parallelism_exceeded", err)
	}
}

// TestCapChunkKeepsTail ensures the per-call output cap keeps the most recent
// bytes and reports truncation, without losing data from the underlying
// buffer (a later poll from an earlier cursor can still reach it).
func TestCapChunkKeepsTail(t *testing.T) {
	chunk := []byte("0123456789")
	got, truncated := capChunk(chunk, 4)
	if !truncated {
		t.Fatal("expected truncated=true")
	}
	if string(got) != "6789" {
		t.Fatalf("capChunk kept %q, want the tail %q", got, "6789")
	}
	if got2, truncated2 := capChunk(chunk, 100); truncated2 || string(got2) != string(chunk) {
		t.Fatalf("capChunk with a cap above len should be a no-op, got %q truncated=%v", got2, truncated2)
	}
	if _, truncated3 := capChunk(chunk, 0); truncated3 {
		t.Fatal("capChunk with n<=0 should disable the cap")
	}
}
