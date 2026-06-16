package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
