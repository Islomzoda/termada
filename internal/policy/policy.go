// Package policy classifies commands as allow / deny / confirm before execution
// (spec §18, SEC-1/SEC-2). Commands are argv arrays and are executed quoted, so
// shell metacharacters are inert (R3); the policy matches on argv, not on a
// re-parsed shell string.
package policy

import (
	"strings"
	"sync"
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
	Allow      []string
	Deny       []string
	Confirm    []string
	AutoAnswer []AutoAnswer
}

// AutoAnswer is a prompt auto-reply, applied only to a confirmed awaiting_input
// state and matched against the prompt tail (spec R7/IN-2).
type AutoAnswer struct {
	Match string
	Send  string
}

// Result is the evaluation outcome.
type Result struct {
	Decision Decision
	Reason   string
	Matched  string
}

// Engine holds the named policies and evaluates commands. Safe for concurrent
// use and supports hot-reload (SEC-4).
type Engine struct {
	mu       sync.RWMutex
	policies map[string]Policy
}

// NewEngine builds an engine from named policies.
func NewEngine(policies map[string]Policy) *Engine {
	if policies == nil {
		policies = map[string]Policy{}
	}
	return &Engine{policies: policies}
}

// Reload atomically replaces the policy set (hot-reload). Callers should
// validate before calling.
func (e *Engine) Reload(policies map[string]Policy) {
	if policies == nil {
		policies = map[string]Policy{}
	}
	e.mu.Lock()
	e.policies = policies
	e.mu.Unlock()
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
