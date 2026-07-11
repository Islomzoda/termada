//go:build unix

package engine

import (
	"strings"
	"testing"
)

func TestSeparateUIDShellEnvironmentDoesNotInheritDaemonSecrets(t *testing.T) {
	t.Setenv("TERMADA_AGENT_TOKEN", "termada-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-secret")
	t.Setenv("SSH_AUTH_SOCK", "/private/agent.sock")
	t.Setenv("BASH_ENV", "/private/injected.sh")

	env := shellEnvironment(SpawnConfig{
		SeparateUID: true,
		UID:         12345,
		GID:         12345,
		Username:    "termada-agent",
		HomeDir:     "/home/termada-agent",
	})
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("malformed environment entry %q", entry)
		}
		values[key] = value
	}
	for _, forbidden := range []string{"TERMADA_AGENT_TOKEN", "AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK", "BASH_ENV"} {
		if _, ok := values[forbidden]; ok {
			t.Fatalf("dropped-uid environment inherited %s", forbidden)
		}
	}
	if values["HOME"] != "/home/termada-agent" || values["USER"] != "termada-agent" || values["LOGNAME"] != "termada-agent" {
		t.Fatalf("dropped identity environment = HOME=%q USER=%q LOGNAME=%q", values["HOME"], values["USER"], values["LOGNAME"])
	}
	if values["PATH"] == "" || values["TERM"] != "xterm-256color" || values["LANG"] != "C" || values["LC_ALL"] != "C" {
		t.Fatalf("minimal shell environment is incomplete: %#v", values)
	}
}

func TestSeparateUIDShellEnvironmentSanitizesIdentityFields(t *testing.T) {
	env := shellEnvironment(SpawnConfig{SeparateUID: true, UID: 12345, GID: 12345, Username: "bad=name", HomeDir: "relative"})
	joined := "\n" + strings.Join(env, "\n") + "\n"
	if !strings.Contains(joined, "\nUSER=12345\n") || !strings.Contains(joined, "\nHOME=/\n") {
		t.Fatalf("unsafe identity fallback missing from %q", joined)
	}
}
