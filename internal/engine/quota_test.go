package engine

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/policy"
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

func TestApprovedBackgroundJobKeepsBackgroundClassification(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxForegroundJobs = 1
	cfg.MaxBackgroundJobs = 1
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"confirm": {Confirm: []string{"sh*"}},
	}), map[string]string{"gate": "confirm"})

	gateSession, err := m.CreateSession("gate", "local", "shell")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := m.Start("gate", gateSession.ID, []string{"sh", "-c", "sleep 5"}, ModeBackground)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Approve(pending.Snapshot().ConfirmationID, "test"); err != nil {
		t.Fatal(err)
	}
	if !pending.isBackground() {
		t.Fatal("approved mode=background job was left in the foreground quota class")
	}

	bgSession, _ := m.CreateSession("other", "local", "shell")
	if _, err := m.Start("other", bgSession.ID, []string{"sleep", "5"}, ModeBackground); err == nil {
		t.Fatal("second background job bypassed MaxBackgroundJobs=1")
	}
	fgSession, _ := m.CreateSession("other", "local", "shell")
	foreground, err := m.Start("other", fgSession.ID, []string{"echo", "ok"}, ModeForeground)
	if err != nil {
		t.Fatalf("background approval incorrectly consumed foreground quota: %v", err)
	}
	waitDone(t, foreground, 5*time.Second)
}

func TestConcurrentApprovalsRespectBackgroundQuota(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxBackgroundJobs = 1
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"confirm": {Confirm: []string{"sh*"}},
	}), map[string]string{"gate": "confirm"})

	confirmations := make([]string, 2)
	for i := range confirmations {
		session, err := m.CreateSession("gate", "local", "shell")
		if err != nil {
			t.Fatal(err)
		}
		job, err := m.Start("gate", session.ID, []string{"sh", "-c", "sleep 5"}, ModeBackground)
		if err != nil {
			t.Fatal(err)
		}
		confirmations[i] = job.Snapshot().ConfirmationID
	}

	var approved, rejected int64
	var wg sync.WaitGroup
	release := make(chan struct{})
	for _, confirmationID := range confirmations {
		wg.Add(1)
		go func(confirmationID string) {
			defer wg.Done()
			<-release
			if err := m.Approve(confirmationID, "test"); err == nil {
				atomic.AddInt64(&approved, 1)
			} else {
				if e, ok := err.(*errs.Error); !ok || e.Code != errs.ParallelismExceeded {
					t.Errorf("Approve: unexpected error %v", err)
				}
				atomic.AddInt64(&rejected, 1)
			}
		}(confirmationID)
	}
	close(release)
	wg.Wait()
	if approved != 1 || rejected != 1 {
		t.Fatalf("approved=%d rejected=%d, want 1/1", approved, rejected)
	}
	if pending := len(m.ListPending()); pending != 1 {
		t.Fatalf("pending confirmations=%d, want rejected approval to remain pending", pending)
	}
	m.mu.Lock()
	active := m.activeBackground()
	m.mu.Unlock()
	if active != 1 {
		t.Fatalf("active background jobs=%d, want 1", active)
	}
}

func TestGlobalJobQuotasConcurrent(t *testing.T) {
	const quota = 2
	const goroutines = 16

	for _, tc := range []struct {
		name string
		mode string
	}{
		{name: "foreground", mode: ModeForeground},
		{name: "background", mode: ModeBackground},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxForegroundJobs = goroutines + 10
			cfg.MaxBackgroundJobs = goroutines + 10
			if tc.mode == ModeBackground {
				cfg.MaxBackgroundJobs = quota
			} else {
				cfg.MaxForegroundJobs = quota
			}
			m := NewManager(cfg)
			t.Cleanup(m.Shutdown)

			sessions := make([]*Session, goroutines)
			for i := range sessions {
				s, err := m.CreateSession("agent", "local", "shell")
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
					<-release
					_, err := m.Start("agent", s.ID, []string{"sleep", "5"}, tc.mode)
					if err == nil {
						atomic.AddInt64(&started, 1)
						return
					}
					if e, ok := err.(*errs.Error); !ok || e.Code != errs.ParallelismExceeded {
						t.Errorf("Start: unexpected error %v, want parallelism_exceeded", err)
					}
					atomic.AddInt64(&rejected, 1)
				}(s)
			}
			close(release)
			wg.Wait()

			if started != quota {
				t.Fatalf("accepted %d Starts, want exactly quota %d", started, quota)
			}
			if started+rejected != goroutines {
				t.Fatalf("accounting: started=%d + rejected=%d != %d", started, rejected, goroutines)
			}
		})
	}
}

func TestSessionReservationsEnforceResourceCaps(t *testing.T) {
	t.Run("per owner concurrent", func(t *testing.T) {
		m := NewManager(DefaultConfig())
		const attempts = 64
		var accepted atomic.Int64
		var wg sync.WaitGroup
		release := make(chan struct{})
		for i := 0; i < attempts; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-release
				if m.reserveSession("agent") == nil {
					accepted.Add(1)
				}
			}()
		}
		close(release)
		wg.Wait()
		if got := accepted.Load(); got != maxSessionsPerOwner {
			t.Fatalf("accepted %d reservations, want %d", got, maxSessionsPerOwner)
		}
		for i := int64(0); i < accepted.Load(); i++ {
			m.releaseSessionReservation("agent")
		}
	})

	t.Run("global", func(t *testing.T) {
		m := NewManager(DefaultConfig())
		for i := 0; i < maxSessionsTotal; i++ {
			if err := m.reserveSession(fmt.Sprintf("agent-%d", i)); err != nil {
				t.Fatalf("reservation %d: %v", i, err)
			}
		}
		if err := m.reserveSession("overflow"); err == nil {
			t.Fatal("global session limit was bypassed")
		}
	})
}

func TestAutoBackgroundRespectsBackgroundQuota(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxForegroundJobs = 1
	cfg.MaxBackgroundJobs = 1
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)

	backgroundSession, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatal(err)
	}
	background, err := m.Start("agent", backgroundSession.ID, []string{"sleep", "5"}, ModeBackground)
	if err != nil {
		t.Fatal(err)
	}

	foregroundSession, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.Run("agent", foregroundSession.ID, []string{"sleep", "5"}, ModeForeground, 20)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusBackgrounded {
		t.Fatalf("status = %s, want backgrounded", result.Status)
	}
	if !strings.Contains(result.Reason, "background quota is full") {
		t.Fatalf("reason = %q, want background-quota explanation", result.Reason)
	}
	foreground, err := m.getJob("agent", result.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if foreground.isBackground() {
		t.Fatal("timed-out job moved into an already-full background pool")
	}

	thirdSession, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("agent", thirdSession.ID, []string{"sleep", "5"}, ModeForeground); err == nil {
		t.Fatal("foreground slot was released even though background quota was full")
	} else if e, ok := err.(*errs.Error); !ok || e.Code != errs.ParallelismExceeded {
		t.Fatalf("error = %v, want parallelism_exceeded", err)
	}

	_ = m.Kill("agent", background.ID)
	_ = m.Kill("agent", foreground.ID)
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
	if got := m.ResolveAgent("bogus", "fallback"); got != "" {
		t.Fatalf("unknown token = %q, want fail-closed empty identity", got)
	}
	if got := m.ResolveAgent("", "claude-code"); got != "" {
		t.Fatalf("tokenless protected id = %q, want fail-closed empty identity", got)
	}
}

func TestCommandAndAgentIdentityBounds(t *testing.T) {
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	if _, err := m.Start("agent", "", nil, ModeForeground); err == nil {
		t.Fatal("empty command was accepted")
	}
	if _, err := m.Start("agent", "", []string{strings.Repeat("x", maxCommandBytes+1)}, ModeForeground); err == nil {
		t.Fatal("oversized command was accepted")
	}
	if _, err := m.Start("agent", "", []string{"echo", "safe\x00; touch injected"}, ModeForeground); err == nil {
		t.Fatal("command containing NUL was accepted")
	}
	if _, err := m.Start("agent", "", []string{"true"}, "bogus"); err == nil {
		t.Fatal("unknown execution mode was accepted")
	}
	if _, err := m.AuthenticateAgent("", strings.Repeat("a", maxAgentIDBytes+1)); err == nil {
		t.Fatal("oversized agent identity was accepted")
	}
}

func TestPendingConfirmationsHaveHardPerOwnerCap(t *testing.T) {
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{"confirm": {Confirm: []string{"*"}}}), map[string]string{"agent": "confirm"})
	for i := 0; i < maxPendingPerOwner; i++ {
		if _, err := m.Start("agent", "", []string{"echo", fmt.Sprint(i)}, ModeForeground); err != nil {
			t.Fatalf("pending %d: %v", i, err)
		}
	}
	if _, err := m.Start("agent", "", []string{"echo", "overflow"}, ModeForeground); err == nil {
		t.Fatal("pending-confirmation cap was bypassed")
	} else if e, ok := err.(*errs.Error); !ok || e.Code != errs.ParallelismExceeded {
		t.Fatalf("overflow error = %v", err)
	}
}
