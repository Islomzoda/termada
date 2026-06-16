package engine

import (
	"testing"
	"time"

	"github.com/termada/termada/internal/errs"
)

func TestPerAgentQuota(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxJobsPerAgent = 2
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)

	start := func(agent string) (*Job, error) {
		s, err := m.CreateSession(agent, "local", "shell")
		if err != nil {
			t.Fatalf("session: %v", err)
		}
		return m.Start(agent, s.ID, []string{"sleep", "5"}, "")
	}
	if _, err := start("A"); err != nil {
		t.Fatalf("job 1: %v", err)
	}
	if _, err := start("A"); err != nil {
		t.Fatalf("job 2: %v", err)
	}
	// third concurrent job for A exceeds the quota
	if _, err := start("A"); err == nil {
		t.Fatal("expected quota error on agent A's 3rd job")
	} else if e, ok := err.(*errs.Error); !ok || e.Code != errs.ParallelismExceeded {
		t.Fatalf("err = %v, want parallelism_exceeded", err)
	}
	// a different agent is unaffected
	sb, _ := m.CreateSession("B", "local", "shell")
	jb, err := m.Start("B", sb.ID, []string{"echo", "ok"}, "")
	if err != nil {
		t.Fatalf("agent B blocked by A's quota: %v", err)
	}
	select {
	case <-jb.Done():
	case <-time.After(5 * time.Second):
	}
}

func TestResolveAgentNonSpoofable(t *testing.T) {
	m := NewManager(DefaultConfig())
	m.SetAgentTokens(map[string]string{"secret-tok": "claude-code"})
	if got := m.ResolveAgent("secret-tok", "spoofed"); got != "claude-code" {
		t.Fatalf("token resolve = %q, want claude-code", got)
	}
	if got := m.ResolveAgent("", "fallback"); got != "fallback" {
		t.Fatalf("no token = %q, want fallback", got)
	}
	if got := m.ResolveAgent("bogus", "fallback"); got != "fallback" {
		t.Fatalf("unknown token = %q, want fallback", got)
	}
}
