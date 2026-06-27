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

	// Allowlists match the command as written, plus a basename form so an absolute
	// path (/bin/ls) satisfies a bare `ls` rule. We deliberately do NOT unwrap
	// wrappers here: `sudo ls` must not pass a whitelist of `ls` — it fails the
	// whitelist and is denied, which is the safe direction.
	if len(p.Allow) > 0 {
		if m := firstMatchT(p.Allow, matchTargets(argv)); m != "" {
			return Result{Decision: Allow, Matched: m}
		}
		return Result{Decision: Deny, Reason: "not in allowlist"}
	}

	// Deny/confirm match an EXPANDED target set: the command, its basename form,
	// and the same for any wrapped command it carries (sudo/env/nice/nohup/… and
	// `<shell> -c "…"`). This stops a deny/confirm rule from being dodged by
	// `sudo rm`, `env X=1 rm`, `/bin/rm` or `bash -c "rm …"`. Matching is
	// case-sensitive, as Unix commands are — case-folding is intentionally avoided
	// (it would over-deny, or worse over-allow a whitelist).
	gate := expandTargets(argv)
	if m := firstMatchT(p.Deny, gate); m != "" {
		return Result{Decision: Deny, Reason: "matched deny rule", Matched: m}
	}
	if m := firstMatchT(p.Confirm, gate); m != "" {
		return Result{Decision: Confirm, Reason: "matched confirm rule", Matched: m}
	}
	return Result{Decision: Allow}
}

// matchTargets returns the strings a rule is matched against for one argv: the
// full command line and the program name, each also with the program's basename
// substituted so an absolute path (/bin/rm) matches a bare-name rule (rm).
func matchTargets(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	full := strings.Join(argv, " ")
	prog := argv[0]
	t := []string{full, prog}
	if base := baseName(prog); base != prog {
		t = append(t, base, base+full[len(prog):])
	}
	return t
}

// expandTargets is matchTargets over argv plus every wrapped command it carries,
// so deny/confirm rules see through sudo/env/nice/nohup/setsid/time/command and
// `<shell> -c "…"`. Unwrapping is bounded in depth.
func expandTargets(argv []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(av []string) {
		for _, s := range matchTargets(av) {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	for cur, depth := argv, 0; len(cur) > 0 && depth < 6; depth++ {
		add(cur)
		inner, ok := unwrapOne(cur)
		if !ok {
			break
		}
		cur = inner
	}
	return out
}

// unwrapOne strips one layer of a known command wrapper, returning the inner
// command it would run: `<shell> -c "<line>"` (the line is tokenised) and prefix
// wrappers (sudo/doas/env/nice/nohup/setsid/stdbuf/ionice/time/command) by
// skipping the wrapper and its option/assignment args. ok=false if not a wrapper.
func unwrapOne(argv []string) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	switch baseName(argv[0]) {
	case "bash", "sh", "zsh", "dash", "ash", "ksh":
		for i := 1; i < len(argv)-1; i++ {
			if argv[i] == "-c" {
				if f := strings.Fields(argv[i+1]); len(f) > 0 {
					return f, true
				}
			}
		}
		return nil, false
	case "sudo", "doas", "env", "nice", "nohup", "setsid", "stdbuf", "ionice", "time", "command":
		i := 1
		for i < len(argv) {
			if a := argv[i]; strings.HasPrefix(a, "-") || isAssignment(a) {
				i++
				continue
			}
			break
		}
		if i < len(argv) {
			return argv[i:], true
		}
		return nil, false
	}
	return nil, false
}

// isAssignment reports whether s looks like a VAR=value env assignment.
func isAssignment(s string) bool {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return false
	}
	return !strings.ContainsAny(s[:eq], "/ \t")
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// firstMatchT returns the first glob in patterns that matches any of targets.
func firstMatchT(patterns, targets []string) string {
	for _, pat := range patterns {
		for _, s := range targets {
			if matchGlob(pat, s) {
				return pat
			}
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
