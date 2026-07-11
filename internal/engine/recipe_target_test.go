package engine

import (
	"os/exec"
	"testing"

	"github.com/termada/termada/internal/errs"
)

// A recipe that declares a remote target must not silently run on a local/default
// session: a mismatched session is refused, and with no session an ad-hoc remote
// one is opened for the target (and closed afterwards).
func TestRunRecipeHonorsTarget(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetRemoteDialer(func(target string, cols, rows int) (ShellConn, error) {
		return startShell(cols, rows, SpawnConfig{})
	})
	m.SetRecipes(map[string]Recipe{
		"deploy": {Name: "deploy", Target: "prod", Steps: [][]string{{"true"}}},
		"local":  {Name: "local", Target: "local", Steps: [][]string{{"true"}}},
	})

	// A local session passed to a prod-targeted recipe → fail loud.
	loc, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create local session: %v", err)
	}
	_, err = m.RunRecipe("agent", loc.ID, "deploy")
	if err == nil {
		t.Fatal("expected target-mismatch error, got nil")
	}
	if e, ok := err.(*errs.Error); !ok || e.Code != errs.InvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}

	remote, err := m.CreateSession("agent", "prod", "shell")
	if err != nil {
		t.Fatalf("create remote session: %v", err)
	}
	if _, err := m.RunRecipe("agent", remote.ID, "local"); err == nil {
		t.Fatal("local-target recipe accepted a remote session")
	}
	if _, err := m.RunRecipe("other", remote.ID, "deploy"); err == nil {
		t.Fatal("recipe accepted another owner's session")
	}
	if err := m.CloseSession("agent", remote.ID); err != nil {
		t.Fatal(err)
	}

	// No session + target → an ad-hoc remote session is opened, used, and closed.
	res, err := m.RunRecipe("agent", "", "deploy")
	if err != nil {
		t.Fatalf("run remote recipe: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("recipe status = %s, want ok", res.Status)
	}
	for _, s := range m.ListSessions() {
		if s.Target == "prod" {
			t.Fatalf("ad-hoc remote session %s was left open", s.SessionID)
		}
	}
}
