package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/termada/termada/internal/bus"
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

// An agent write to a live command that is not waiting for input must not be
// allowed to sit in the PTY queue. Otherwise the persistent shell executes it as
// a fresh command after the current job's completion marker, bypassing policy.
func TestWriteRejectsQueuedIdleShellCommand(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"sleep", "0.5"}, ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "queued-command-ran")
	err = m.Write("agent", job.ID, "touch "+shellQuote(marker), true, false, false)
	structured, ok := err.(*errs.Error)
	if !ok || structured.Code != errs.DeniedByPolicy {
		t.Fatalf("write to non-interactive current job = %v, want denied_by_policy", err)
	}
	waitDone(t, job, 5*time.Second)

	// Run one more command in the same shell to establish that it has consumed all
	// terminal input preceding this point. A leaked line would have run first.
	barrier, err := m.Start("agent", sess.ID, []string{"true"}, ModeForeground)
	if err != nil {
		t.Fatalf("start barrier: %v", err)
	}
	waitDone(t, barrier, 5*time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("queued input executed as an idle-shell command: %v", err)
	}
}

func TestSessionAndFileOwnershipIsolation(t *testing.T) {
	m := newTestManager(t)
	victim, err := m.CreateSession("victim", "local", "shell")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "owned.txt")
	if err := os.WriteFile(path, []byte("victim-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Start("attacker", victim.ID, []string{"echo", "stolen"}, ""); err == nil {
		t.Fatal("attacker started a job in victim session")
	}
	if got := m.ListSessionsFor("attacker"); len(got) != 0 {
		t.Fatalf("attacker session list leaked %+v", got)
	}
	if got := m.ListSessionsFor("victim"); len(got) != 1 || got[0].SessionID != victim.ID {
		t.Fatalf("victim session list = %+v", got)
	}
	if _, err := m.FileReadFor("attacker", victim.ID, path, 0); err == nil {
		t.Fatal("attacker read through victim session")
	}
	if _, err := m.FileWriteFor("attacker", victim.ID, path, "changed", ""); err == nil {
		t.Fatal("attacker wrote through victim session")
	}
	if _, err := m.SessionTailFor("attacker", victim.ID, ""); err == nil {
		t.Fatal("attacker tailed victim session")
	}
	if err := m.SessionWriteInputFor("attacker", victim.ID, "echo bypass", true, false); err == nil {
		t.Fatal("attacker wrote directly to victim PTY")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "victim-data" {
		t.Fatalf("victim file changed: %q err=%v", b, err)
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

// TestCapChunkKeepsHead ensures capped output remains sequential: callers can
// advance by the returned bytes and fetch the remainder on the next poll.
func TestCapChunkKeepsHead(t *testing.T) {
	chunk := []byte("0123456789")
	got, truncated := capChunk(chunk, 4)
	if !truncated {
		t.Fatal("expected truncated=true")
	}
	if string(got) != "0123" {
		t.Fatalf("capChunk kept %q, want the head %q", got, "0123")
	}
	if got2, truncated2 := capChunk(chunk, 100); truncated2 || string(got2) != string(chunk) {
		t.Fatalf("capChunk with a cap above len should be a no-op, got %q truncated=%v", got2, truncated2)
	}
	if _, truncated3 := capChunk(chunk, 0); truncated3 {
		t.Fatal("capChunk with n<=0 should disable the cap")
	}
}

func TestCappedOutputCursorIsSequential(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxOutputBytes = 4
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)

	run, err := m.Run("agent", "", []string{"printf", "0123456789"}, ModeForeground, 5_000)
	if err != nil {
		t.Fatal(err)
	}
	if run.Stdout != "0123" || !run.HasMore || run.NextCursor != "4" {
		t.Fatalf("first page = stdout=%q has_more=%v cursor=%q", run.Stdout, run.HasMore, run.NextCursor)
	}
	p2, err := m.Poll("agent", run.JobID, run.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if p2.StdoutChunk != "4567" || !p2.HasMore || p2.NextCursor != "8" {
		t.Fatalf("second page = chunk=%q has_more=%v cursor=%q", p2.StdoutChunk, p2.HasMore, p2.NextCursor)
	}
	p3, err := m.Poll("agent", run.JobID, p2.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if p3.StdoutChunk != "89" || p3.HasMore || p3.NextCursor != "10" {
		t.Fatalf("final page = chunk=%q has_more=%v cursor=%q", p3.StdoutChunk, p3.HasMore, p3.NextCursor)
	}
}

func TestConfirmationJobsRespectQuotas(t *testing.T) {
	t.Run("enqueue counts against per-agent quota", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.MaxJobsPerAgent = 1
		m := NewManager(cfg)
		t.Cleanup(m.Shutdown)
		m.SetPolicy(policy.NewEngine(map[string]policy.Policy{"p": {Confirm: []string{"danger*"}}}), map[string]string{"agent": "p"})
		s1, _ := m.CreateSession("agent", "local", "shell")
		s2, _ := m.CreateSession("agent", "local", "shell")
		if _, err := m.Start("agent", s1.ID, []string{"danger-one"}, ""); err != nil {
			t.Fatalf("first confirmation: %v", err)
		}
		if _, err := m.Start("agent", s2.ID, []string{"danger-two"}, ""); err == nil {
			t.Fatal("second parked confirmation bypassed per-agent quota")
		}
	})

	t.Run("approve respects foreground quota", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.MaxForegroundJobs = 1
		m := NewManager(cfg)
		t.Cleanup(m.Shutdown)
		m.SetPolicy(policy.NewEngine(map[string]policy.Policy{"p": {Confirm: []string{"danger*"}}}), map[string]string{"gate": "p"})
		busySession, _ := m.CreateSession("busy", "local", "shell")
		busy, err := m.Start("busy", busySession.ID, []string{"sleep", "5"}, ModeForeground)
		if err != nil {
			t.Fatal(err)
		}
		gateSession, _ := m.CreateSession("gate", "local", "shell")
		pending, err := m.Start("gate", gateSession.ID, []string{"danger-cmd"}, ModeForeground)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Approve(pending.Snapshot().ConfirmationID, "test"); err == nil {
			t.Fatal("approval bypassed max foreground jobs")
		}
		_ = m.Kill("busy", busy.ID)
		waitDone(t, busy, 5*time.Second)
		if err := m.Approve(pending.Snapshot().ConfirmationID, "test"); err != nil {
			t.Fatalf("approval should succeed after slot is free: %v", err)
		}
	})
}

func TestApproveRechecksAuditHealth(t *testing.T) {
	m := newTestManager(t)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{"p": {Confirm: []string{"danger*"}}}), map[string]string{"agent": "p"})
	healthy := true
	m.SetAuditHealth(func() bool { return healthy })
	pending, err := m.Start("agent", "", []string{"danger-cmd"}, ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	cid := pending.Snapshot().ConfirmationID
	healthy = false
	if err := m.Approve(cid, "test"); err == nil {
		t.Fatal("approval executed after audit became unhealthy")
	}
	if len(m.ListPending()) != 1 {
		t.Fatal("failed approval should remain pending for a later healthy retry or denial")
	}
	healthy = true
	if err := m.Approve(cid, "test"); err != nil {
		t.Fatalf("healthy retry: %v", err)
	}
}

func TestConfirmationFailsClosedOnSynchronousAuditError(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		m := newTestManager(t)
		m.SetPolicy(policy.NewEngine(map[string]policy.Policy{"p": {Confirm: []string{"danger*"}}}), map[string]string{"agent": "p"})
		b := bus.New(10)
		m.SetBus(b)
		cancel := b.SubscribeReliable(func(bus.Event) error { return errors.New("disk full") })
		defer cancel()
		if _, err := m.Start("agent", "", []string{"danger-cmd"}, ModeForeground); err == nil {
			t.Fatal("confirmation request succeeded without a durable audit record")
		}
		if len(m.ListPending()) != 0 {
			t.Fatal("failed confirmation request remained pending")
		}
	})

	t.Run("approval", func(t *testing.T) {
		m := newTestManager(t)
		m.SetPolicy(policy.NewEngine(map[string]policy.Policy{"p": {Confirm: []string{"sh*"}}}), map[string]string{"agent": "p"})
		b := bus.New(10)
		m.SetBus(b)
		cancel := b.SubscribeReliable(func(event bus.Event) error {
			if event.Type == bus.EvConfirmResolved {
				return errors.New("disk full")
			}
			return nil
		})
		defer cancel()
		marker := filepath.Join(t.TempDir(), "ran")
		pending, err := m.Start("agent", "", []string{"sh", "-c", "echo ran > " + marker}, ModeForeground)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Approve(pending.Snapshot().ConfirmationID, "test"); err == nil {
			t.Fatal("approval succeeded without a durable audit record")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("dangerous command ran despite audit failure: %v", err)
		}
	})
}

func TestOrdinaryCommandFailsClosedOnAuditIntentError(t *testing.T) {
	m := newTestManager(t)
	b := bus.New(10)
	m.SetBus(b)
	cancel := b.SubscribeReliable(func(event bus.Event) error {
		if event.Type == bus.EvJobStartRequested {
			return errors.New("disk full")
		}
		return nil
	})
	defer cancel()
	marker := filepath.Join(t.TempDir(), "ran")
	if _, err := m.Start("agent", "", []string{"sh", "-c", "echo ran > " + marker}, ModeForeground); err == nil {
		t.Fatal("ordinary command started without a durable audit intent")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran despite audit failure: %v", err)
	}
}

func TestShutdownResolvesPendingConfirmations(t *testing.T) {
	m := NewManager(DefaultConfig())
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{"p": {Confirm: []string{"danger*"}}}), map[string]string{"agent": "p"})
	job, err := m.Start("agent", "", []string{"danger-cmd"}, ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	m.Shutdown()
	select {
	case <-job.Done():
	case <-time.After(time.Second):
		t.Fatal("pending confirmation remained live after Shutdown")
	}
	if got := len(m.ListPending()); got != 0 {
		t.Fatalf("Shutdown left %d pending confirmations", got)
	}
}

func TestKillCancelsPendingConfirmation(t *testing.T) {
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{"p": {Confirm: []string{"danger*"}}}), map[string]string{"agent": "p"})
	job, err := m.Start("agent", "", []string{"danger-cmd"}, ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Kill("agent", job.ID); err != nil {
		t.Fatal(err)
	}
	if got := job.Snapshot().Status; got != StatusKilled {
		t.Fatalf("status = %s, want killed", got)
	}
	if got := len(m.ListPending()); got != 0 {
		t.Fatalf("kill left %d confirmations pending", got)
	}
}
