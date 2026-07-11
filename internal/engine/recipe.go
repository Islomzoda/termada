package engine

import (
	"fmt"
	"sort"
	"time"

	"github.com/termada/termada/internal/errs"
)

const recipeInterruptGrace = 5 * time.Second

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
	Command   []string `json:"command"`
	Status    Status   `json:"status"`
	ExitCode  *int     `json:"exit_code,omitempty"`
	Stdout    string   `json:"stdout"`
	JobID     string   `json:"job_id,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
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
	if target := r.Target; target != "" {
		if sessionID != "" {
			st, _, err := m.sessionTargetFor(owner, sessionID)
			if err != nil {
				return nil, err
			}
			if st != target {
				return nil, errs.New(errs.InvalidArgument,
					"recipe %q targets %q but session %s targets %q — run it without a session or pass one on %q", name, target, sessionID, st, target)
			}
		} else if target != "local" {
			sess, err := m.CreateSession(owner, target, "shell")
			if err != nil {
				return nil, err
			}
			sessionID = sess.ID
			defer func() { _ = m.CloseSession(owner, sessionID) }()
		}
	}

	res := &RecipeRunResult{Recipe: name, Status: "ok"}
	remainingOutput := m.maxOutputBytes()
	for _, step := range r.Steps {
		job, err := m.Start(owner, sessionID, step, ModeForeground)
		if err != nil {
			res.Steps = append(res.Steps, RecipeStepResult{Command: step, Status: StatusFailed, Stdout: err.Error()})
			res.Status = "failed"
			break
		}
		timeout := m.recipeStepTimeout
		if timeout <= 0 {
			timeout = 30 * time.Minute
		}
		timer := time.NewTimer(timeout)
		timedOut := false
		forcedSessionClose := false
		var interruptErr error
		select {
		case <-job.Done():
			timer.Stop()
		case <-timer.C:
			timedOut = true
			forcedSessionClose, interruptErr = m.interruptRecipeJob(owner, job, recipeInterruptGrace)
		}
		info := job.info()
		var chunk []byte
		var gap, capped bool
		if remainingOutput > 0 {
			chunk, _, gap, capped = job.clean.ReadFromLimit(0, remainingOutput)
			remainingOutput -= len(chunk)
		} else if job.clean.Total() > 0 {
			capped = true
		}
		reason := info.Reason
		if timedOut {
			switch {
			case forcedSessionClose && job.sess.Target != "local":
				reason = fmt.Sprintf("recipe step exceeded %s; interrupt did not stop it, so the SSH session was closed (the remote process may still continue)", timeout)
			case forcedSessionClose:
				reason = fmt.Sprintf("recipe step exceeded %s; interrupt did not stop it, so the session was closed", timeout)
			default:
				reason = fmt.Sprintf("recipe step exceeded %s and was interrupted", timeout)
			}
			if interruptErr != nil {
				reason += fmt.Sprintf(" (interrupt error: %v)", interruptErr)
			}
		}
		res.Steps = append(res.Steps, RecipeStepResult{
			Command: step, Status: info.Status, ExitCode: info.ExitCode, Stdout: string(chunk),
			JobID: job.ID, Reason: reason, Truncated: gap || capped,
		})
		if timedOut || info.Status != StatusExited || (info.ExitCode != nil && *info.ExitCode != 0) {
			res.Status = "failed"
			break
		}
	}
	return res, nil
}

// interruptRecipeJob first asks the backend to terminate the command, then
// gives it a bounded grace period. SSH signals are best-effort Ctrl-C, so a job
// that remains live after the grace period cannot be left occupying the shared
// session/quota after RunRecipe returns: close the transport and force a local
// terminal state. The remote-process caveat is surfaced by the caller.
func (m *Manager) interruptRecipeJob(owner string, job *Job, grace time.Duration) (forcedSessionClose bool, interruptErr error) {
	interruptErr = m.Kill(owner, job.ID)
	if interruptErr == nil && grace > 0 {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-job.Done():
			return false, nil
		case <-timer.C:
		}
	} else {
		select {
		case <-job.Done():
			return false, interruptErr
		default:
		}
	}

	if err := m.CloseSession(owner, job.SessionID); err != nil {
		// A concurrent close may already have removed it from the registry. The
		// job retains its session pointer, so closing it is still idempotent and
		// guarantees its local lifecycle becomes terminal.
		job.sess.close()
	}
	select {
	case <-job.Done():
	default:
		job.finalize(-1, StatusOrphaned, "recipe timeout closed a non-responsive session")
	}
	return true, interruptErr
}
