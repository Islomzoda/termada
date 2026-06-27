package mcp

import (
	"io"
	"log"
	"strings"
	"testing"

	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/errs"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	return NewServer(NewLocalBackend(m), "tester", "test", log.New(io.Discard, "", 0))
}

func callMap(t *testing.T, s *Server, name string, args map[string]any) map[string]any {
	t.Helper()
	res, e := s.tools[name].Handler(args)
	if e != nil {
		t.Fatalf("%s returned error: %v", name, e)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", name, res)
	}
	return m
}

func argv(xs ...string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// exec_run output must carry signal only: status/exit_code/stdout, and NONE of
// the redundant echo (command) or false operator flags.
func TestExecRunIsLean(t *testing.T) {
	s := newTestServer(t)
	out := callMap(t, s, "exec_run", map[string]any{"command": argv("echo", "hi")})

	if out["status"] != engine.StatusExited {
		t.Fatalf("status = %v, want exited", out["status"])
	}
	if out["exit_code"] != 0 {
		t.Fatalf("exit_code = %v, want 0", out["exit_code"])
	}
	if s, _ := out["stdout"].(string); s == "" {
		t.Fatalf("stdout empty: %v", out)
	}
	for _, k := range []string{"command", "hold_input", "hold_output", "awaiting_input", "job_id", "truncated"} {
		if _, present := out[k]; present {
			t.Fatalf("lean exec_run should not include %q for a finished command: %v", k, out)
		}
	}
}

// Errors must come back with an actionable hint so an agent recovers in one shot.
func TestErrorsCarryHint(t *testing.T) {
	s := newTestServer(t)
	_, e := s.tools["exec_poll"].Handler(map[string]any{"job_id": "job_does_not_exist"})
	if e == nil {
		t.Fatal("expected an error polling a missing job")
	}
	if e.Code != errs.NotFound {
		t.Fatalf("code = %v, want not_found", e.Code)
	}
	if e.Hint == "" {
		t.Fatal("error has no hint")
	}
}

// session_busy (the classic footgun) must explain how to recover.
func TestSessionBusyHint(t *testing.T) {
	s := newTestServer(t)
	if _, e := s.tools["exec_start"].Handler(map[string]any{"command": argv("sleep", "5")}); e != nil {
		t.Fatalf("start: %v", e)
	}
	_, e := s.tools["exec_run"].Handler(map[string]any{"command": argv("echo", "x")})
	if e == nil || e.Code != errs.SessionBusy {
		t.Fatalf("expected session_busy, got %v", e)
	}
	if e.Hint == "" {
		t.Fatal("session_busy has no hint")
	}
}

// exec_list returns newest-first, lean entries, with an omitted count when capped.
func TestExecListNewestFirstAndCapped(t *testing.T) {
	s := newTestServer(t)
	callMap(t, s, "exec_run", map[string]any{"command": argv("echo", "one")})
	callMap(t, s, "exec_run", map[string]any{"command": argv("echo", "two")})

	out := callMap(t, s, "exec_list", map[string]any{"limit": 1})
	jobs, _ := out["jobs"].([]map[string]any)
	if len(jobs) != 1 {
		t.Fatalf("limit=1 returned %d jobs", len(jobs))
	}
	cmd, _ := jobs[0]["command"].([]string)
	if len(cmd) < 2 || cmd[1] != "two" {
		t.Fatalf("newest-first broken: top job = %v", jobs[0]["command"])
	}
	if _, ok := out["omitted"]; !ok {
		t.Fatalf("expected omitted count when capped: %v", out)
	}
	for _, k := range []string{"hold_input", "hold_output"} {
		if _, present := jobs[0][k]; present {
			t.Fatalf("list entry should not include operator flag %q", k)
		}
	}
}

// A parked confirm-job stays non-terminal (the agent keeps polling), so a poll
// must still surface confirmation_id — consistent with exec_run/exec_start.
func TestLeanPollKeepsConfirmationID(t *testing.T) {
	pr := &engine.PollResult{Info: engine.Info{
		JobID:          "job_x",
		Status:         engine.StatusAwaitingConfirmation,
		ConfirmationID: "cnf_abc",
	}}
	m := leanPoll(pr)
	if m["confirmation_id"] != "cnf_abc" {
		t.Fatalf("leanPoll dropped confirmation_id for a parked confirm-job: %v", m)
	}
	if _, ok := m["next_cursor"]; !ok {
		t.Fatalf("non-terminal poll should include next_cursor: %v", m)
	}
}

func TestCapabilitiesHasQuickstart(t *testing.T) {
	s := newTestServer(t)
	out := callMap(t, s, "capabilities", map[string]any{})
	if q, _ := out["quickstart"].(string); q == "" {
		t.Fatal("capabilities missing quickstart cheatsheet")
	}
}

// In-process (no daemon) the agent must be told remote is unavailable, instead
// of the old stale "SSH/fleet/vault are phase 2" note that made it give up and
// silently use a local shell.
func TestCapabilitiesReportsInProcessMode(t *testing.T) {
	s := newTestServer(t)
	out := callMap(t, s, "capabilities", map[string]any{})
	if r, _ := out["remote"].(bool); r {
		t.Fatalf("in-process capabilities should report remote=false: %v", out)
	}
	if m, _ := out["exec_mode"].(string); m != "in-process" {
		t.Fatalf("exec_mode = %q, want in-process", m)
	}
	if n, _ := out["notes"].(string); strings.Contains(n, "phase 2") {
		t.Fatalf("notes still claim the stale phase-2 status: %q", n)
	}
}

// A remote target with no daemon must fail LOUDLY (so the agent doesn't fall
// through to exec_run's silent local default session) and the error must say how
// to enable remote.
func TestRemoteSessionRejectedInProcess(t *testing.T) {
	s := newTestServer(t)
	_, e := s.tools["session_create"].Handler(map[string]any{"target": "ravand.pro"})
	if e == nil {
		t.Fatal("expected an error creating a remote session in-process")
	}
	if e.Code != errs.NotSupported {
		t.Fatalf("code = %v, want not_supported", e.Code)
	}
	// local must still work.
	if _, e := s.tools["session_create"].Handler(map[string]any{"target": "local"}); e != nil {
		t.Fatalf("local session_create should still work: %v", e)
	}
}
