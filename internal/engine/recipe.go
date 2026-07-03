package engine

import (
	"sort"
	"time"

	"github.com/termada/termada/internal/errs"
)

// Recipe is a named macro of command steps (spec §19/RC-1).
type Recipe struct {
	Name   string
	Target string
	Steps  [][]string
}

// RecipeInfo is the JSON-facing recipe description.
type RecipeInfo struct {
	Name   string     `json:"name"`
	Target string     `json:"target,omitempty"`
	Steps  [][]string `json:"steps"`
}

// RecipeStepResult is the outcome of one step.
type RecipeStepResult struct {
	Command  []string `json:"command"`
	Status   Status   `json:"status"`
	ExitCode *int     `json:"exit_code,omitempty"`
	Stdout   string   `json:"stdout"`
}

// RecipeRunResult is the outcome of running a recipe.
type RecipeRunResult struct {
	Recipe string             `json:"recipe"`
	Status string             `json:"status"` // ok | failed
	Steps  []RecipeStepResult `json:"steps"`
}

// SetRecipes installs the configured recipes.
func (m *Manager) SetRecipes(recipes map[string]Recipe) {
	m.mu.Lock()
	m.recipes = recipes
	m.mu.Unlock()
}

// RecipeList returns the configured recipes.
func (m *Manager) RecipeList() []RecipeInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecipeInfo, 0, len(m.recipes))
	for _, r := range m.recipes {
		out = append(out, RecipeInfo{Name: r.Name, Target: r.Target, Steps: r.Steps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RunRecipe executes a recipe's steps sequentially in a session, stopping on the
// first failure. Each step is policy-checked individually (spec FL-3/§19). It is
// synchronous; recipes are expected to be finite commands, not daemons.
func (m *Manager) RunRecipe(owner, sessionID, name string) (*RecipeRunResult, error) {
	m.mu.Lock()
	r, ok := m.recipes[name]
	m.mu.Unlock()
	if !ok {
		return nil, errs.New(errs.NotFound, "recipe %s not found", name)
	}

	// Honor the recipe's declared target so a recipe written for a remote server
	// can't silently run on the local/default session (the silent-wrong-host
	// class of bug). With a target set: a passed session must match it (else fail
	// loud), and with no session we open an ad-hoc one on the target and close it
	// after the run.
	if target := r.Target; target != "" && target != "local" {
		if sessionID != "" {
			st, ok := m.SessionTarget(sessionID)
			if !ok {
				return nil, errs.New(errs.NotFound, "session %s not found", sessionID)
			}
			if st != target {
				return nil, errs.New(errs.InvalidArgument,
					"recipe %q targets %q but session %s targets %q — run it without a session or pass one on %q", name, target, sessionID, st, target)
			}
		} else {
			sess, err := m.CreateSession(owner, target, "shell")
			if err != nil {
				return nil, err
			}
			sessionID = sess.ID
			defer func() { _ = m.CloseSession(owner, sessionID) }()
		}
	}

	res := &RecipeRunResult{Recipe: name, Status: "ok"}
	for _, step := range r.Steps {
		job, err := m.Start(owner, sessionID, step, ModeForeground)
		if err != nil {
			res.Steps = append(res.Steps, RecipeStepResult{Command: step, Status: StatusFailed, Stdout: err.Error()})
			res.Status = "failed"
			break
		}
		select {
		case <-job.Done():
		case <-time.After(30 * time.Minute):
		}
		info := job.info()
		chunk, _, _ := job.clean.ReadFrom(0)
		res.Steps = append(res.Steps, RecipeStepResult{
			Command: step, Status: info.Status, ExitCode: info.ExitCode, Stdout: string(chunk),
		})
		if info.Status != StatusExited || (info.ExitCode != nil && *info.ExitCode != 0) {
			res.Status = "failed"
			break
		}
	}
	return res, nil
}
