package policy

import "testing"

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
