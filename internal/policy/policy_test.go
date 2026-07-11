package policy

import (
	"runtime"
	"testing"
)

func engine() *Engine {
	return NewEngine(map[string]Policy{
		"read-only": {Allow: []string{"ls", "cat", "git status", "docker ps"}, Deny: []string{"*"}},
		"prod-safe": {Deny: []string{"rm -rf /", "DROP DATABASE"}, Confirm: []string{"rm -rf*", "systemctl stop*"}},
	})
}

func TestAllowlistWhitelist(t *testing.T) {
	e := engine()
	if d := e.Evaluate("read-only", []string{"ls", "-la"}).Decision; d != Allow {
		t.Fatalf("ls => %s, want allow", d)
	}
	if d := e.Evaluate("read-only", []string{"git", "status"}).Decision; d != Allow {
		t.Fatalf("git status => %s, want allow", d)
	}
	if d := e.Evaluate("read-only", []string{"rm", "-rf", "x"}).Decision; d != Deny {
		t.Fatalf("rm => %s, want deny", d)
	}
}

// A deny/confirm rule must not be dodged by a wrapper or an absolute path.
func TestNormalizationClosesBypasses(t *testing.T) {
	e := engine()
	denied := [][]string{
		{"/bin/rm", "-rf", "/"},          // absolute path
		{"sudo", "rm", "-rf", "/"},       // sudo wrapper
		{"env", "X=1", "rm", "-rf", "/"}, // env assignment + wrapper
		{"nice", "rm", "-rf", "/"},       // nice wrapper
		{"bash", "-c", "rm -rf /"},       // shell -c payload
		{"sudo", "/bin/rm", "-rf", "/"},  // wrapper + absolute path
	}
	for _, av := range denied {
		if d := e.Evaluate("prod-safe", av).Decision; d != Deny {
			t.Fatalf("%v => %s, want deny", av, d)
		}
	}
	if d := e.Evaluate("prod-safe", []string{"sudo", "rm", "-rf", "build"}).Decision; d != Confirm {
		t.Fatalf("sudo rm -rf build => %s, want confirm", d)
	}
	// An absolute path satisfies an allowlist's bare name…
	if d := e.Evaluate("read-only", []string{"/bin/ls", "-la"}).Decision; d != Allow {
		t.Fatalf("/bin/ls => %s, want allow", d)
	}
	// …but a wrapper must NOT slip a non-allowed program past the whitelist.
	if d := e.Evaluate("read-only", []string{"sudo", "ls"}).Decision; d != Deny {
		t.Fatalf("sudo ls (allowlist) => %s, want deny", d)
	}
}

func TestCompoundShellCommandsFailClosed(t *testing.T) {
	e := NewEngine(map[string]Policy{
		"deny":    {Deny: []string{"rm -rf /"}},
		"confirm": {Confirm: []string{"rm -rf*"}},
	})
	denied := [][]string{
		{"bash", "-c", "echo safe; rm -rf /"},
		{"bash", "-lc", "echo safe && rm -rf /"},
		{"sh", "-ec", "echo safe\nrm -rf /"},
		{"bash", "-c", "echo safe; r''m -rf /"},
		{"bash", "-c", "$DANGEROUS_COMMAND"},
		{"bash", "-c", "eval rm -rf /"},
		{"bash", "-c", "command eval rm -rf /"},
		{"bash", "-c", "source /tmp/unreviewed-script"},
		{"sudo", "-u", "root", "env", "-u", "HOME", "bash", "-lc", "echo safe || rm -rf /"},
	}
	for _, av := range denied {
		if got := e.Evaluate("deny", av); got.Decision != Deny {
			t.Errorf("%v => %+v, want deny", av, got)
		}
	}

	if got := e.Evaluate("confirm", []string{"bash", "-c", "echo safe; rm -rf build"}); got.Decision != Confirm {
		t.Fatalf("compound confirm command => %+v, want confirm", got)
	}
	// Even when no literal rule target can be extracted, ambiguous shell syntax
	// cannot silently become allow under a confirm policy.
	if got := e.Evaluate("confirm", []string{"bash", "-c", "$MAYBE_DANGEROUS"}); got.Decision != Confirm {
		t.Fatalf("dynamic shell command => %+v, want confirm", got)
	}
}

func TestUninspectableShellInvocationsFailClosed(t *testing.T) {
	e := NewEngine(map[string]Policy{
		"deny":    {Deny: []string{"rm -rf /"}},
		"confirm": {Confirm: []string{"rm -rf*"}},
	})
	commands := map[string][]string{
		"script":      {"bash", "/tmp/agent-script"},
		"stdin":       {"bash", "-s"},
		"interactive": {"bash"},
	}
	for name, argv := range commands {
		t.Run(name, func(t *testing.T) {
			if got := e.Evaluate("deny", argv); got.Decision != Deny {
				t.Fatalf("deny policy evaluated %v as %+v, want deny", argv, got)
			}
			if got := e.Evaluate("confirm", argv); got.Decision != Confirm {
				t.Fatalf("confirm policy evaluated %v as %+v, want confirm", argv, got)
			}
		})
	}

	// An explicit, syntax-free -c payload remains inspectable and can proceed
	// when it does not match any restrictive rule.
	if got := e.Evaluate("deny", []string{"bash", "-c", "printf ok"}); got.Decision != Allow {
		t.Fatalf("inspectable shell payload = %+v, want allow", got)
	}
}

func TestEnvSplitStringIsOpaque(t *testing.T) {
	for name, rules := range map[string]Policy{
		"deny":    {Deny: []string{"touch *"}},
		"confirm": {Confirm: []string{"touch *"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := NewEngine(map[string]Policy{"p": rules})
			for _, argv := range [][]string{
				{"env", "-S", "touch /tmp/policy-bypass", "true"},
				{"env", "-Stouch /tmp/policy-bypass", "true"},
				{"env", "--split-string=touch /tmp/policy-bypass", "true"},
			} {
				got := e.Evaluate("p", argv).Decision
				if name == "deny" && got != Deny {
					t.Fatalf("Evaluate(%q) = %s, want deny", argv, got)
				}
				if name == "confirm" && got != Confirm {
					t.Fatalf("Evaluate(%q) = %s, want confirm", argv, got)
				}
			}
		})
	}
}

func TestWrapperOptionParsing(t *testing.T) {
	e := NewEngine(map[string]Policy{"p": {Deny: []string{"rm -rf /"}}})
	for _, av := range [][]string{
		{"sudo", "-uroot", "/bin/rm", "-rf", "/"},
		{"sudo", "--user=root", "env", "X=1", "rm", "-rf", "/"},
		{"nice", "-n", "10", "nohup", "rm", "-rf", "/"},
	} {
		if got := e.Evaluate("p", av); got.Decision != Deny {
			t.Errorf("%v => %+v, want deny", av, got)
		}
	}
}

func TestLeadingAssignmentCannotHideDeniedProgram(t *testing.T) {
	e := NewEngine(map[string]Policy{"p": {Deny: []string{"rm -rf /"}}})
	if got := e.Evaluate("p", []string{"X=1", "rm", "-rf", "/"}); got.Decision != Deny {
		t.Fatalf("assignment-prefixed command = %+v, want deny", got)
	}
}

func TestDarwinExecutableMatchingFoldsOnlyProgramCase(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("APFS/HFS executable case-folding is Darwin-specific")
	}
	e := NewEngine(map[string]Policy{"p": {Deny: []string{"rm -rf /", "bash*"}}})
	for _, command := range [][]string{{"/BIN/RM", "-rf", "/"}, {"/BIN/BASH", "-c", "echo ok"}} {
		if got := e.Evaluate("p", command); got.Decision != Deny {
			t.Fatalf("alternate-case executable %v = %+v, want deny", command, got)
		}
	}
}

func TestDarwinAllowlistRequiresExactExecutableCase(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin-specific allowlist regression")
	}
	e := NewEngine(map[string]Policy{"p": {Allow: []string{"ls"}}})
	for _, command := range [][]string{{"LS"}, {"/BIN/LS"}} {
		if got := e.Evaluate("p", command); got.Decision != Deny {
			t.Fatalf("alternate-case allowlisted executable %v = %+v, want deny", command, got)
		}
	}
	if got := e.Evaluate("p", []string{"/bin/ls"}); got.Decision != Allow {
		t.Fatalf("exact-case allowlisted executable = %+v, want allow", got)
	}
}

func TestDenyAndConfirm(t *testing.T) {
	e := engine()
	if d := e.Evaluate("prod-safe", []string{"rm", "-rf", "/"}).Decision; d != Deny {
		t.Fatalf("rm -rf / => %s, want deny", d)
	}
	if d := e.Evaluate("prod-safe", []string{"rm", "-rf", "build"}).Decision; d != Confirm {
		t.Fatalf("rm -rf build => %s, want confirm", d)
	}
	if d := e.Evaluate("prod-safe", []string{"ls"}).Decision; d != Allow {
		t.Fatalf("ls => %s, want allow", d)
	}
	if d := e.Evaluate("prod-safe", []string{"systemctl", "stop", "nginx"}).Decision; d != Confirm {
		t.Fatalf("systemctl stop => %s, want confirm", d)
	}
}

func TestNoPolicyAllows(t *testing.T) {
	e := engine()
	if d := e.Evaluate("", []string{"anything"}).Decision; d != Allow {
		t.Fatalf("empty policy => %s, want allow", d)
	}
	if d := e.Evaluate("missing", []string{"anything"}).Decision; d != Allow {
		t.Fatalf("unknown policy => %s, want allow", d)
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"rm -rf*", "rm -rf build", true},
		{"rm -rf*", "rm -r build", false},
		{"*.sh", "deploy.sh", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*", "whatever", true},
		{"git status", "git status", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.pat, c.s); got != c.want {
			t.Errorf("matchGlob(%q,%q)=%v want %v", c.pat, c.s, got, c.want)
		}
	}
}

func TestManagedPolicyStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	store := dir + "/policies.json"

	// Engine with one config-defined policy + a managed store.
	e := NewEngine(map[string]Policy{"prod-safe": {Deny: []string{"rm -rf /"}}})
	e.LoadStore(store)

	// Add a managed policy.
	if err := e.Set("restricted", Policy{Allow: []string{"ls", "git status"}, Deny: []string{"*"}}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !e.Managed()["restricted"] {
		t.Fatal("restricted should be managed")
	}
	if e.Managed()["prod-safe"] {
		t.Fatal("config policy must not be reported as managed")
	}
	// It evaluates immediately.
	if d := e.Evaluate("restricted", []string{"rm", "-rf", "x"}).Decision; d != Deny {
		t.Fatalf("restricted rm => %s, want deny", d)
	}

	// Config-defined policies are read-only via the API.
	if err := e.Set("prod-safe", Policy{Allow: []string{"*"}}); err == nil {
		t.Fatal("editing a config policy should fail")
	}
	if err := e.Remove("prod-safe"); err == nil {
		t.Fatal("removing a config policy should fail")
	}
	if err := e.Remove("nope"); err == nil {
		t.Fatal("removing an unknown policy should fail")
	}

	// Name validation.
	if err := e.Set("bad name!", Policy{}); err == nil {
		t.Fatal("invalid name should fail")
	}
	if err := e.Set("", Policy{}); err == nil {
		t.Fatal("empty name should fail")
	}

	// Persistence: a fresh engine loading the same store sees the managed policy.
	e2 := NewEngine(map[string]Policy{"prod-safe": {Deny: []string{"rm -rf /"}}})
	e2.LoadStore(store)
	if !e2.Managed()["restricted"] {
		t.Fatal("managed policy did not persist across reload")
	}
	if d := e2.Evaluate("restricted", []string{"ls"}).Decision; d != Allow {
		t.Fatalf("reloaded restricted ls => %s, want allow", d)
	}

	// Update + delete.
	if err := e2.Set("restricted", Policy{Deny: []string{"curl*"}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := e2.Remove("restricted"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if e2.Managed()["restricted"] {
		t.Fatal("restricted should be gone after remove")
	}
	// And the deletion persisted.
	e3 := NewEngine(nil)
	e3.LoadStore(store)
	if e3.Managed()["restricted"] {
		t.Fatal("deletion did not persist")
	}
}
