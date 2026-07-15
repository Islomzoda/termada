package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/policy"
)

func TestCloseSessionCancelsPendingConfirmationExactlyOnce(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfirmTimeoutMS = 2_000
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)
	events := bus.New(100)
	m.SetBus(events)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"review": {Confirm: []string{"echo*"}},
	}), map[string]string{"agent": "review"})

	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	type pendingCase struct {
		job     *Job
		confirm *pendingConfirm
	}
	cases := make([]pendingCase, 0, 2)
	for _, label := range []string{"first", "second"} {
		job, startErr := m.Start("agent", sess.ID, []string{"echo", label}, ModeForeground)
		if startErr != nil {
			t.Fatalf("start %s pending job: %v", label, startErr)
		}
		cid := job.Snapshot().ConfirmationID
		if cid == "" {
			t.Fatalf("%s pending job has no confirmation id", label)
		}
		m.mu.Lock()
		confirmation := m.pending[cid]
		m.mu.Unlock()
		if confirmation == nil || confirmation.timer == nil {
			t.Fatalf("%s pending confirmation timer was not installed", label)
		}
		cases = append(cases, pendingCase{job: job, confirm: confirmation})
	}
	if err := m.CloseSession("agent", sess.ID); err != nil {
		t.Fatalf("close session: %v", err)
	}
	if pending := m.ListPending(); len(pending) != 0 {
		t.Fatalf("pending after close = %+v", pending)
	}
	resolved := make(map[string]int, len(cases))
	for _, event := range events.Recent(100) {
		if event.Type != bus.EvConfirmResolved {
			continue
		}
		resolved[event.JobID]++
		if event.Data["approved"] != false || event.Data["by"] != "session_close" || event.Data["reason"] != "session closed" {
			t.Fatalf("close resolution event = %+v", event)
		}
	}
	for _, tc := range cases {
		if tc.confirm.timer.Stop() {
			t.Fatalf("CloseSession left confirmation timer %s active", tc.confirm.ID)
		}
		waitDone(t, tc.job, time.Second)
		info := tc.job.Snapshot()
		if info.Status != StatusFailed || !strings.Contains(info.Reason, "session closed") {
			t.Fatalf("closed pending job = %+v", info)
		}
		if err := m.Approve(tc.confirm.ID, "dashboard"); err == nil {
			t.Fatalf("approval %s succeeded after session close", tc.confirm.ID)
		} else if structured, ok := err.(*errs.Error); !ok || structured.Code != errs.NotFound {
			t.Fatalf("approval %s after close = %v, want not_found", tc.confirm.ID, err)
		}
		if resolved[tc.job.ID] != 1 {
			t.Fatalf("job %s resolved events = %d, want 1", tc.job.ID, resolved[tc.job.ID])
		}
	}
}

func TestHoldRejectsInactiveJobsAndSkipsNoopEvents(t *testing.T) {
	m := newTestManager(t)
	events := bus.New(100)
	m.SetBus(events)
	yes := true

	finished, err := m.Start("agent", "", []string{"true"}, ModeForeground)
	if err != nil {
		t.Fatalf("start terminal job: %v", err)
	}
	waitDone(t, finished, 5*time.Second)
	if err := m.Hold(finished.ID, &yes, nil); err == nil {
		t.Fatal("terminal job accepted a hold")
	} else if structured, ok := err.(*errs.Error); !ok || structured.Code != errs.NotFound {
		t.Fatalf("terminal hold = %v, want not_found", err)
	}
	if finished.Snapshot().HoldInput {
		t.Fatal("terminal hold mutated job state")
	}

	running, err := m.Start("agent", "", []string{"bash", "-c", "read -r value"}, ModeForeground)
	if err != nil {
		t.Fatalf("start running job: %v", err)
	}
	if err := m.Hold(running.ID, &yes, nil); err != nil {
		t.Fatalf("first hold: %v", err)
	}
	if err := m.Hold(running.ID, &yes, nil); err != nil {
		t.Fatalf("idempotent hold: %v", err)
	}
	holdEvents := 0
	for _, event := range events.Recent(100) {
		if event.Type == "job.hold" && event.JobID == running.ID {
			holdEvents++
		}
	}
	if holdEvents != 1 {
		t.Fatalf("job.hold events = %d, want 1", holdEvents)
	}
	if err := m.Write("agent", running.ID, "done", true, false, true); err != nil {
		t.Fatalf("finish running job: %v", err)
	}
	waitDone(t, running, 5*time.Second)

	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"review": {Confirm: []string{"echo*"}},
	}), map[string]string{"agent": "review"})
	pending, err := m.Start("agent", "", []string{"echo", "pending"}, ModeForeground)
	if err != nil {
		t.Fatalf("start pending job: %v", err)
	}
	if err := m.Hold(pending.ID, &yes, nil); err == nil {
		t.Fatal("awaiting-confirmation job accepted a hold")
	} else if structured, ok := err.(*errs.Error); !ok || structured.Code != errs.NotFound {
		t.Fatalf("pending hold = %v, want not_found", err)
	}
	if pending.Snapshot().HoldInput {
		t.Fatal("pending hold mutated job state")
	}
	if err := m.Deny(pending.Snapshot().ConfirmationID, "cleanup"); err != nil {
		t.Fatalf("deny pending job: %v", err)
	}
}

func TestSessionMetadataHidesUnregisteredCurrentJobs(t *testing.T) {
	m := newTestManager(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	internalJob, err := sess.runRaw("sleep 5", nil, "init")
	if err != nil {
		t.Fatalf("start internal job: %v", err)
	}

	sessions := m.ListSessionsFor("agent")
	if len(sessions) != 1 || sessions[0].ActiveJobs != 0 || sessions[0].CurrentJobID != "" {
		t.Fatalf("session exposed internal job %s: %+v", internalJob.ID, sessions)
	}
	tail, err := m.SessionTailFor("agent", sess.ID, "")
	if err != nil {
		t.Fatalf("session tail: %v", err)
	}
	if tail.JobID != "" || tail.AwaitingInput || tail.Prompt != "" {
		t.Fatalf("session stream exposed internal job %s: %+v", internalJob.ID, tail)
	}
	if err := m.CloseSession("agent", sess.ID); err != nil {
		t.Fatalf("close session: %v", err)
	}
	waitDone(t, internalJob, time.Second)

	liveSession, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create live session: %v", err)
	}
	liveJob, err := m.Start("agent", liveSession.ID, []string{"sleep", "5"}, ModeForeground)
	if err != nil {
		t.Fatalf("start registered job: %v", err)
	}
	t.Cleanup(func() { _ = m.CloseSession("agent", liveSession.ID) })
	sessions = m.ListSessionsFor("agent")
	if len(sessions) != 1 || sessions[0].ActiveJobs != 1 || sessions[0].CurrentJobID != liveJob.ID {
		t.Fatalf("registered current job missing from session metadata: %+v", sessions)
	}
	tail, err = m.SessionTailFor("agent", liveSession.ID, "")
	if err != nil {
		t.Fatalf("live session tail: %v", err)
	}
	if tail.JobID != liveJob.ID {
		t.Fatalf("session stream job id = %q, want %q", tail.JobID, liveJob.ID)
	}
}

func TestRecoveredJobsAreNotStreamAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	stored := []Info{{
		JobID: "job_recovered", Owner: "agent", Status: StatusExited,
		CreatedUnixMS: time.Now().UnixMilli(), StreamAvailable: true,
	}}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	m := newTestManager(t)
	if err := m.EnablePersistence(path); err != nil {
		t.Fatalf("enable persistence: %v", err)
	}
	jobs := m.ListJobs("agent", "all")
	if len(jobs) != 1 || jobs[0].StreamAvailable {
		t.Fatalf("recovered stream metadata = %+v", jobs)
	}

	live, err := m.Start("agent", "", []string{"true"}, ModeForeground)
	if err != nil {
		t.Fatalf("start live job: %v", err)
	}
	waitDone(t, live, 5*time.Second)
	if !live.Snapshot().StreamAvailable {
		t.Fatal("in-memory terminal job is not streamable")
	}
}

func TestJobTailHandleDrainsAfterRegistryCleanup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxOutputBytes = 4
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)

	target, err := m.Start("agent", "", []string{"printf", "0123456789"}, ModeForeground)
	if err != nil {
		t.Fatalf("start target job: %v", err)
	}
	waitDone(t, target, 5*time.Second)
	tail, err := m.OpenJobTailFor("agent", target.ID)
	if err != nil {
		t.Fatalf("open job tail: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	newer, err := m.Start("agent", "", []string{"true"}, ModeForeground)
	if err != nil {
		t.Fatalf("start newer job: %v", err)
	}
	waitDone(t, newer, 5*time.Second)

	m.GCOnce(0, 1)
	if _, err := m.PollFor("agent", target.ID, "", true); err == nil {
		t.Fatal("registry poll found a job that GC removed")
	} else if structured, ok := err.(*errs.Error); !ok || structured.Code != errs.NotFound {
		t.Fatalf("registry poll after GC = %v, want not_found", err)
	}

	var outputText strings.Builder
	cursor := ""
	done := false
	for page := 0; page < 8 && !done; page++ {
		result, pollErr := tail.Poll(cursor, true)
		if pollErr != nil {
			t.Fatalf("stable tail page %d: %v", page, pollErr)
		}
		outputText.WriteString(result.StdoutChunk)
		cursor = result.NextCursor
		done = result.Status.Terminal() && !result.HasMore
	}
	if !done {
		t.Fatal("stable tail did not reach the terminal cursor")
	}
	if got := outputText.String(); !strings.Contains(got, "0123456789") {
		t.Fatalf("stable tail output = %q, want complete target output", got)
	}
}
