package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/policy"
)

func TestPolicyDeny(t *testing.T) {
	m := newTestManager(t)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"p": {Deny: []string{"rm*"}},
	}), map[string]string{"agent": "p"})

	_, err := m.Start("agent", "", []string{"rm", "-rf", "x"}, "")
	e, ok := err.(*errs.Error)
	if !ok || e.Code != errs.DeniedByPolicy {
		t.Fatalf("expected denied_by_policy, got %v", err)
	}
}

func TestPolicyConfirmApprove(t *testing.T) {
	m := newTestManager(t)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"p": {Confirm: []string{"echo*"}},
	}), map[string]string{"agent": "p"})

	job, err := m.Start("agent", "", []string{"echo", "approved"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	snap := job.Snapshot()
	if snap.Status != StatusAwaitingConfirmation {
		t.Fatalf("status = %s, want awaiting_confirmation", snap.Status)
	}
	if snap.ConfirmationID == "" {
		t.Fatalf("missing confirmation_id")
	}
	pending := m.ListPending()
	if len(pending) != 1 || pending[0].ConfirmationID != snap.ConfirmationID {
		t.Fatalf("pending = %+v", pending)
	}

	if err := m.Approve(snap.ConfirmationID, "tester"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	waitDone(t, job, 5*time.Second)
	res := poll(t, m, job.ID)
	if res.Status != StatusExited || !strings.Contains(res.StdoutChunk, "approved") {
		t.Fatalf("after approve: status=%s out=%q", res.Status, res.StdoutChunk)
	}
}

func TestPolicyConfirmDeny(t *testing.T) {
	m := newTestManager(t)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"p": {Confirm: []string{"echo*"}},
	}), map[string]string{"agent": "p"})

	job, err := m.Start("agent", "", []string{"echo", "nope"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	cid := job.Snapshot().ConfirmationID
	if err := m.Deny(cid, "tester"); err != nil {
		t.Fatalf("deny: %v", err)
	}
	waitDone(t, job, 5*time.Second)
	if s := job.Snapshot().Status; s != StatusFailed {
		t.Fatalf("status = %s, want failed", s)
	}
	if len(m.ListPending()) != 0 {
		t.Fatalf("pending should be empty after deny")
	}
}

func TestPendingInfoUsesActualDeadlineAndNormalizedMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfirmTimeoutMS = 5_000
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"p": {Confirm: []string{"echo*"}},
	}), map[string]string{"agent": "p"})

	job, err := m.Start("agent", "", []string{"echo", "pending"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.Deny(job.Snapshot().ConfirmationID, "cleanup") })
	pending := m.ListPending()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want one", pending)
	}
	m.mu.Lock()
	actualDeadline := m.pending[pending[0].ConfirmationID].Deadline
	m.mu.Unlock()
	if pending[0].Mode != ModeAuto || pending[0].Mode != job.Snapshot().Mode {
		t.Fatalf("pending mode = %q, job mode = %q", pending[0].Mode, job.Snapshot().Mode)
	}
	if pending[0].ExpiresUnixMS != actualDeadline.UnixMilli() || pending[0].ExpiresUnix != actualDeadline.Unix() {
		t.Fatalf("pending deadline = %+v, actual = %s", pending[0], actualDeadline)
	}
	if pending[0].RequestedUnixMS <= 0 || pending[0].RequestedUnixMS/1000 != pending[0].RequestedUnix {
		t.Fatalf("pending request timestamps = %+v", pending[0])
	}
}
