package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileWriteRead(t *testing.T) {
	m := newTestManager(t)
	path := filepath.Join(t.TempDir(), "note.txt")
	wr, err := m.FileWrite(path, "hello file", "truncate")
	if err != nil || !wr.OK || wr.Bytes != 10 {
		t.Fatalf("write: %+v err=%v", wr, err)
	}
	rd, err := m.FileRead(path, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rd.Content != "hello file" || rd.Truncated {
		t.Fatalf("read = %+v", rd)
	}
}

func TestFileReadTruncates(t *testing.T) {
	m := newTestManager(t)
	path := filepath.Join(t.TempDir(), "big.txt")
	_ = os.WriteFile(path, []byte("0123456789ABCDEF"), 0o644)
	rd, err := m.FileRead(path, 5)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rd.Content != "01234" || !rd.Truncated {
		t.Fatalf("expected truncation, got %+v", rd)
	}
}

func TestFileReadRedacts(t *testing.T) {
	m := newTestManager(t)
	path := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(path, []byte("token ghp_abcdefghijklmnopqrstuvwxyz0"), 0o644)
	rd, _ := m.FileRead(path, 0)
	if strings.Contains(rd.Content, "ghp_abcdefghijklmnopqrstuvwxyz0") {
		t.Fatalf("secret not redacted in file_read: %q", rd.Content)
	}
}

func TestRecipeRun(t *testing.T) {
	m := newTestManager(t)
	dir := t.TempDir()
	m.SetRecipes(map[string]Recipe{
		"setup": {Name: "setup", Steps: [][]string{
			{"cd", dir},
			{"touch", "marker"},
			{"echo", "done"},
		}},
	})
	if list := m.RecipeList(); len(list) != 1 || list[0].Name != "setup" {
		t.Fatalf("recipe list = %+v", list)
	}
	res, err := m.RunRecipe("agent", "", "setup")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "ok" || len(res.Steps) != 3 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err != nil {
		t.Fatalf("recipe did not create marker: %v", err)
	}
}

func TestRecipeStopsOnFailure(t *testing.T) {
	m := newTestManager(t)
	m.SetRecipes(map[string]Recipe{
		"bad": {Name: "bad", Steps: [][]string{
			{"false"},
			{"echo", "should-not-run"},
		}},
	})
	res, err := m.RunRecipe("agent", "", "bad")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "failed" || len(res.Steps) != 1 {
		t.Fatalf("expected to stop after first failing step, got %+v", res)
	}
}

func TestRecipeOutputIsCapped(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxOutputBytes = 4
	m := NewManager(cfg)
	t.Cleanup(m.Shutdown)
	m.SetRecipes(map[string]Recipe{"out": {Name: "out", Steps: [][]string{{"printf", "123456789"}}}})
	res, err := m.RunRecipe("agent", "", "out")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Steps[0]; got.Stdout != "1234" || !got.Truncated || got.JobID == "" {
		t.Fatalf("bounded recipe step = %+v", got)
	}
}

func TestRecipeTimeoutInterruptsStep(t *testing.T) {
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.recipeStepTimeout = 50 * time.Millisecond
	m.SetRecipes(map[string]Recipe{"slow": {Name: "slow", Steps: [][]string{{"sleep", "5"}}}})
	start := time.Now()
	res, err := m.RunRecipe("agent", "", "slow")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || len(res.Steps) != 1 || !strings.Contains(res.Steps[0].Reason, "exceeded") {
		t.Fatalf("timed-out recipe = %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("recipe timeout took %v", elapsed)
	}
}

type nonResponsiveRecipeShell struct{ closed bool }

func (s *nonResponsiveRecipeShell) Read([]byte) (int, error)    { return 0, os.ErrClosed }
func (s *nonResponsiveRecipeShell) Write(p []byte) (int, error) { return len(p), nil }
func (s *nonResponsiveRecipeShell) Signal(string) error         { return nil }
func (s *nonResponsiveRecipeShell) Close() error {
	s.closed = true
	return nil
}

func TestRecipeTimeoutClosesSessionAfterInterruptGrace(t *testing.T) {
	m := NewManager(DefaultConfig())
	shell := &nonResponsiveRecipeShell{}
	sess := &Session{
		ID: "recipe-session", Owner: "agent", Target: "remote", Mode: "shell",
		cfg: SessionConfig{OutputRetentionBytes: 1024}, redactor: m.redactor, shell: shell,
	}
	job := newJob(sess, []string{"ignore-interrupt"}, ModeForeground)
	job.activate()
	sess.current = job
	m.sessions[sess.ID] = sess
	m.jobs[job.ID] = job

	forced, err := m.interruptRecipeJob("agent", job, time.Millisecond)
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if !forced || !shell.closed {
		t.Fatalf("forced=%v shell.closed=%v, want forced close", forced, shell.closed)
	}
	if !job.info().Status.Terminal() {
		t.Fatalf("job remained live after recipe timeout: %s", job.info().Status)
	}
	if _, ok := m.sessions[sess.ID]; ok {
		t.Fatal("non-responsive recipe session remained registered")
	}
}
