package engine

import (
	"sync"
	"sync/atomic"
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
		return m.Start(agent, s.ID, []string{"sleep", "2"}, "")
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

// TestPerAgentQuotaConcurrent exercises the TOCTOU the sequential TestPerAgentQuota
// can't reach: many goroutines call Start for the SAME agent on DIFFERENT sessions
// at once. Each is on its own session, so the per-session SessionBusy guard never
// fires — only the agent-wide quota gates them. The invariant: the agent's live
// job count must never exceed MaxJobsPerAgent, no matter how the Starts interleave.
func TestPerAgentQuotaConcurrent(t *testing.T) {
	const quota = 3
	const goroutines = 16
	cfg := DefaultConfig()
	cfg.MaxJobsPerAgent = quota
	// Raise the global foreground cap well above the goroutine count so it can't
	// mask the per-agent race by rejecting first.
	cfg.MaxForegroundJobs = goroutines + 10
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)

	// Pre-create one ready session per goroutine: the burst then races purely on
	// the agent-wide quota, not on session creation or SessionBusy.
	sessions := make([]*Session, goroutines)
	for i := range sessions {
		s, err := m.CreateSession("A", "local", "shell")
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
		sessions[i] = s
	}

	var (
		wg                sync.WaitGroup
		started, rejected int64
	)
	release := make(chan struct{})
	for _, s := range sessions {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			<-release // unblock everyone at once to maximise the interleave
			_, err := m.Start("A", s.ID, []string{"sleep", "2"}, "")
			if err != nil {
				if e, ok := err.(*errs.Error); !ok || e.Code != errs.ParallelismExceeded {
					t.Errorf("Start: unexpected error %v, want parallelism_exceeded", err)
				}
				atomic.AddInt64(&rejected, 1)
				return
			}
			atomic.AddInt64(&started, 1)
		}(s)
	}
	close(release)
	wg.Wait()

	// The accepted jobs are all still running `sleep 2`, so the live count equals
	// the number accepted. Before the fix, concurrent Starts could each pass the
	// pre-check and register, pushing this above the quota.
	if got := m.activeForAgent("A"); got > quota {
		t.Fatalf("active jobs for A = %d, exceeds quota %d", got, quota)
	}
	if started > quota {
		t.Fatalf("accepted %d Starts, exceeds quota %d", started, quota)
	}
	// All long-running jobs outlive the burst, so exactly the quota is admitted
	// and the rest rejected — none should slip through, and none should be lost.
	if started != quota {
		t.Fatalf("accepted %d Starts, want exactly the quota %d", started, quota)
	}
	if started+rejected != goroutines {
		t.Fatalf("accounting: started=%d + rejected=%d != %d goroutines", started, rejected, goroutines)
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
