// Package policy classifies commands as allow / deny / confirm before execution
// (spec §18, SEC-1/SEC-2). Commands are argv arrays and are executed quoted, so
// shell metacharacters are inert (R3); the policy matches on argv, not on a
// re-parsed shell string.
package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/termada/termada/internal/errs"
)

// Decision is the outcome of evaluating a command against a policy.
type Decision string

const (
	Allow   Decision = "allow"
	Deny    Decision = "deny"
	Confirm Decision = "confirm"
)

// Policy is a named set of rules.
type Policy struct {
	Allow      []string     `json:"allow,omitempty"`
	Deny       []string     `json:"deny,omitempty"`
	Confirm    []string     `json:"confirm,omitempty"`
	AutoAnswer []AutoAnswer `json:"auto_answer,omitempty"`
}

// AutoAnswer is a prompt auto-reply, applied only to a confirmed awaiting_input
// state and matched against the prompt tail (spec R7/IN-2).
type AutoAnswer struct {
	Match string `json:"match"`
	Send  string `json:"send"`
}

// Result is the evaluation outcome.
type Result struct {
	Decision Decision
	Reason   string
	Matched  string
}

// Engine holds the named policies and evaluates commands. Safe for concurrent
// use and supports hot-reload (SEC-4).
//
// Policies come from two places: config.yaml (the base set, read-only at
// runtime) and the dashboard (managed policies, created/edited/removed live and
// persisted to a JSON store). Managed names are tracked so config-defined
// policies stay read-only — exactly like the fleet's managed servers.
type Engine struct {
	mu        sync.RWMutex
	policies  map[string]Policy
	managed   map[string]bool // names added/edited via the dashboard (persisted)
	storePath string
}

// NewEngine builds an engine from named policies.
func NewEngine(policies map[string]Policy) *Engine {
	if policies == nil {
		policies = map[string]Policy{}
	}
	return &Engine{policies: policies, managed: map[string]bool{}}
}

// Reload atomically replaces the config-defined policy set (hot-reload), keeping
// dashboard-managed policies layered on top. Callers should validate first.
func (e *Engine) Reload(policies map[string]Policy) {
	if policies == nil {
		policies = map[string]Policy{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	merged := make(map[string]Policy, len(policies)+len(e.managed))
	for k, v := range policies {
		merged[k] = v
	}
	for name := range e.managed {
		if p, ok := e.policies[name]; ok {
			merged[name] = p
		}
	}
	e.policies = merged
}

// LoadStore loads dashboard-managed policies from path and merges them over the
// config-defined set (managed wins on a name clash). Sets the path for later
// saves. A missing/garbage file is ignored.
func (e *Engine) LoadStore(path string) {
	e.mu.Lock()
	e.storePath = path
	e.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var managed map[string]Policy
	if json.Unmarshal(data, &managed) != nil {
		return
	}
	e.mu.Lock()
	for name, p := range managed {
		e.policies[name] = p
		e.managed[name] = true
	}
	e.mu.Unlock()
}

// Set creates or updates a dashboard-managed policy and persists it. A name that
// already exists as a config-defined policy is read-only (edit config.yaml).
func (e *Engine) Set(name string, p Policy) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errs.New(errs.InvalidArgument, "policy name is required")
	}
	if !validPolicyName(name) {
		return errs.New(errs.InvalidArgument, "policy name may contain only letters, digits, '-', '_' and '.'")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.policies[name]; exists && !e.managed[name] {
		return errs.New(errs.DeniedByPolicy, "policy %q is defined in config.yaml; edit the file to change it", name)
	}
	e.policies[name] = p
	e.managed[name] = true
	return e.saveLocked()
}

// Remove deletes a dashboard-managed policy. Config-defined policies cannot be
// removed via the API.
func (e *Engine) Remove(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.policies[name]; !exists {
		return errs.New(errs.NotFound, "no such policy: %s", name)
	}
	if !e.managed[name] {
		return errs.New(errs.DeniedByPolicy, "policy %q is defined in config.yaml; edit the file to remove it", name)
	}
	delete(e.policies, name)
	delete(e.managed, name)
	return e.saveLocked()
}

// Managed returns the set of dashboard-managed (editable/removable) policy names.
func (e *Engine) Managed() map[string]bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]bool, len(e.managed))
	for k := range e.managed {
		out[k] = true
	}
	return out
}

// saveLocked persists the managed policies to storePath. Caller holds e.mu.
func (e *Engine) saveLocked() error {
	if e.storePath == "" {
		return nil
	}
	managed := make(map[string]Policy, len(e.managed))
	for name := range e.managed {
		managed[name] = e.policies[name]
	}
	data, err := json.MarshalIndent(managed, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.storePath), 0o700); err != nil {
		return err
	}
	tmp := e.storePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.storePath)
}

// validPolicyName keeps managed names to a safe identifier charset so they can't
// inject markup/quotes into the dashboard.
func validPolicyName(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// Policies returns a shallow copy of the named policy set (read-only view, e.g.
// for the dashboard). The slices inside are shared — treat them as immutable.
func (e *Engine) Policies() map[string]Policy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]Policy, len(e.policies))
	for k, v := range e.policies {
		out[k] = v
	}
	return out
}

// AutoAnswers returns the auto-answer rules for a policy.
func (e *Engine) AutoAnswers(policyName string) []AutoAnswer {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.policies[policyName].AutoAnswer
}

// Evaluate classifies argv against the named policy. An unknown or empty policy
// name means "no policy" → Allow (the local default; restrictive policies are
// opt-in per agent).
//
// Precedence:
//  1. If the policy has an Allow list, it is a whitelist: a match → Allow,
//     anything else → Deny.
//  2. Otherwise a Deny match → Deny.
//  3. Otherwise a Confirm match → Confirm.
//  4. Otherwise → Allow.
func (e *Engine) Evaluate(policyName string, argv []string) Result {
	if policyName == "" || len(argv) == 0 {
		return Result{Decision: Allow}
	}
	e.mu.RLock()
	p, ok := e.policies[policyName]
	e.mu.RUnlock()
	if !ok {
		return Result{Decision: Allow, Reason: "no such policy: " + policyName}
	}

	cmd := strings.Join(argv, " ")
	prog := argv[0]

	if len(p.Allow) > 0 {
		if m := firstMatch(p.Allow, cmd, prog); m != "" {
			return Result{Decision: Allow, Matched: m}
		}
		return Result{Decision: Deny, Reason: "not in allowlist", Matched: ""}
	}
	if m := firstMatch(p.Deny, cmd, prog); m != "" {
		return Result{Decision: Deny, Reason: "matched deny rule", Matched: m}
	}
	if m := firstMatch(p.Confirm, cmd, prog); m != "" {
		return Result{Decision: Confirm, Reason: "matched confirm rule", Matched: m}
	}
	return Result{Decision: Allow}
}

// firstMatch returns the first glob in patterns that matches the full command
// string or the program name.
func firstMatch(patterns []string, cmd, prog string) string {
	for _, pat := range patterns {
		if matchGlob(pat, cmd) || matchGlob(pat, prog) {
			return pat
		}
	}
	return ""
}

// matchGlob is a simple wildcard matcher supporting '*' (any run, including
// empty) and '?' (single char). Unlike path.Match it does not treat '/'
// specially, which suits command matching.
func matchGlob(pattern, s string) bool {
	return globMatch(pattern, s)
}

func globMatch(pat, s string) bool {
	// Iterative wildcard match with backtracking on '*'.
	var pi, si, star, mark int
	star = -1
	for si < len(s) {
		if pi < len(pat) && (pat[pi] == '?' || pat[pi] == s[si]) {
			pi++
			si++
		} else if pi < len(pat) && pat[pi] == '*' {
			star = pi
			mark = si
			pi++
		} else if star != -1 {
			pi = star + 1
			mark++
			si = mark
		} else {
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}
