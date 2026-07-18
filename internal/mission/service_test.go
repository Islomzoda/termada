package mission

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/engine"
)

func newTestService(t *testing.T) (*Service, *engine.Manager, *bus.Bus, string) {
	t.Helper()
	mgr := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(mgr.Shutdown)
	b := bus.New(100)
	mgr.SetBus(b)
	path := filepath.Join(t.TempDir(), "missions.json")
	svc, err := New(path, mgr, b.Publish, func(value string) string { return strings.ReplaceAll(value, "secret", "***") })
	if err != nil {
		t.Fatal(err)
	}
	cancel := b.SubscribeReliable(svc.RecordEvent)
	t.Cleanup(cancel)
	return svc, mgr, b, path
}

func TestMissionRequiresSuccessfulJobEvidence(t *testing.T) {
	svc, mgr, _, _ := newTestService(t)
	mission, err := svc.Create("codex", CreateRequest{Goal: "repair the demo service", Plan: []string{"diagnose", "verify"}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := mgr.Start("codex", mission.SessionID, []string{"true"}, engine.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("job did not finish")
	}
	if _, err := svc.Update("codex", mission.ID, UpdateRequest{StepID: "step_1", StepStatus: StepPassed}); err == nil {
		t.Fatal("passed step without job evidence was accepted")
	}
	if _, err := svc.Update("codex", mission.ID, UpdateRequest{StepID: "step_1", StepStatus: StepPassed, JobID: job.ID, Note: "real check"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update("codex", mission.ID, UpdateRequest{Status: StatusSucceeded}); err == nil {
		t.Fatal("mission succeeded with an unfinished step")
	}
	if _, err := svc.Update("codex", mission.ID, UpdateRequest{StepID: "step_2", StepStatus: StepPassed, JobID: job.ID}); err != nil {
		t.Fatal(err)
	}
	finished, err := svc.Update("codex", mission.ID, UpdateRequest{Status: StatusSucceeded, Summary: "service repaired and verified"})
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != StatusSucceeded || finished.CompletedAt == nil {
		t.Fatalf("finished mission = %#v", finished)
	}
	report := ReportMarkdown(finished)
	for _, required := range []string{"# Evidence Report", "job.finished", job.ID, "service repaired and verified", "termada audit verify"} {
		if !strings.Contains(report, required) {
			t.Fatalf("report missing %q:\n%s", required, report)
		}
	}
}

func TestMissionApprovalAndRestartRecovery(t *testing.T) {
	svc, _, b, path := newTestService(t)
	mission, err := svc.Create("codex", CreateRequest{Goal: "deploy safely", Target: "local", Plan: []string{"deploy"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(bus.Event{Type: bus.EvConfirmRequested, AgentID: "codex", SessionID: mission.SessionID, JobID: "job_approval", Message: "./deploy.sh", Data: map[string]any{"confirmation_id": "cnf_1"}}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get("codex", mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusNeedsAttention || got.Events[len(got.Events)-1].ConfirmationID != "cnf_1" {
		t.Fatalf("approval not correlated: %#v", got)
	}

	mgr2 := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(mgr2.Shutdown)
	reloaded, err := New(path, mgr2, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reloaded.Get("codex", mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != StatusInterrupted {
		t.Fatalf("recovered status = %q", recovered.Status)
	}
	resumed, err := reloaded.Resume("codex", mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != StatusRunning || resumed.SessionID == mission.SessionID {
		t.Fatalf("resumed mission = %#v", resumed)
	}
}

func TestMissionOwnerIsolationAndRedaction(t *testing.T) {
	svc, _, b, _ := newTestService(t)
	mission, err := svc.Create("codex-a", CreateRequest{Goal: "inspect", Plan: []string{"inspect"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get("codex-b", mission.ID); err == nil {
		t.Fatal("other owner read mission")
	}
	if err := b.Publish(bus.Event{Type: bus.EvJobStarted, AgentID: "codex-a", SessionID: mission.SessionID, JobID: "job_1", Message: "echo secret"}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get("codex-a", mission.ID)
	last := got.Events[len(got.Events)-1]
	if strings.Contains(last.Message, "secret") || !strings.Contains(last.Message, "***") {
		t.Fatalf("event not redacted: %#v", last)
	}
}
