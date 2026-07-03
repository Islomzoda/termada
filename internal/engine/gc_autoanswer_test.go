package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/termada/termada/internal/policy"
)

func TestGCPrunesTerminalJobs(t *testing.T) {
	m := newTestManager(t)
	var last *Job
	for i := 0; i < 3; i++ {
		j, err := m.Start("agent", "", []string{"true"}, "")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		waitDone(t, j, 5*time.Second)
		last = j
	}
	// keep at most 1 terminal job
	m.GCOnce(0, 1)
	if n := len(m.ListJobs("agent", "all")); n > 1 {
		t.Fatalf("after GC(maxKeep=1) there are %d jobs, want <=1", n)
	}
	_ = last
}

func TestAutoAnswerPrompt(t *testing.T) {
	m := newTestManager(t)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"p": {AutoAnswer: []policy.AutoAnswer{{Match: "Continue?", Send: "yes"}}},
	}), map[string]string{"agent": "p"})

	job, err := m.Start("agent", "", []string{"bash", "-c", "printf 'Continue? '; read -r x; echo got=$x"}, "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, job, 8*time.Second)
	res := poll(t, m, job.ID)
	if !strings.Contains(res.StdoutChunk, "got=yes") {
		t.Fatalf("auto-answer did not fire, output = %q", res.StdoutChunk)
	}
}
