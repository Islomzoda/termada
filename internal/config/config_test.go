package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(here), "..", "..", "config.example.yaml")
	if _, err := Load(path); err != nil {
		t.Fatalf("config.example.yaml does not match the strict schema: %v", err)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadExpandsExplicitEnvironmentReferences(t *testing.T) {
	t.Setenv("TERMADA_TEST_TOKEN", "0123456789abcdef")
	path := writeConfig(t, `
agents:
  - id: ci
    token: ${TERMADA_TEST_TOKEN}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Agents[0].Token; got != "0123456789abcdef" {
		t.Fatalf("expanded token = %q", got)
	}
}

func TestEnvironmentExpansionCannotInjectYAML(t *testing.T) {
	value := "literal # value\npolicies:\n  injected: {}"
	t.Setenv("TERMADA_TEST_VALUE", value)
	path := writeConfig(t, `
notifications:
  telegram:
    bot_token: ${TERMADA_TEST_VALUE}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Notifications.Telegram.BotToken; got != value {
		t.Fatalf("expanded value = %q, want exact environment value", got)
	}
	if _, ok := cfg.Policies["injected"]; ok {
		t.Fatal("environment value injected a YAML policy")
	}
}

func TestEnvironmentReferencesInCommentsAreIgnored(t *testing.T) {
	path := writeConfig(t, "# optional token: ${TERMADA_COMMENT_ONLY_MISSING}\n")
	if _, err := Load(path); err != nil {
		t.Fatalf("comment-only environment reference caused an error: %v", err)
	}
}

func TestLoadRejectsUnsetEnvironmentReference(t *testing.T) {
	path := writeConfig(t, "agents:\n  - id: ci\n    token: ${TERMADA_MISSING_TEST_TOKEN}\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "TERMADA_MISSING_TEST_TOKEN") {
		t.Fatalf("Load error = %v, want missing environment variable", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, "dashboard:\n  enabld: true\n")
	if _, err := Load(path); err == nil {
		t.Fatal("unknown YAML field should fail")
	}
}

func TestLoadRejectsMultipleYAMLDocuments(t *testing.T) {
	path := writeConfig(t, "dashboard:\n  enabled: true\n---\ndashboard:\n  enabled: false\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("multiple YAML documents error = %v", err)
	}
}

func TestLoadRejectsUnsupportedOrUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"backend", "vault:\n  backend: keychain\n", "vault.backend"},
		{"unlock ttl", "vault:\n  unlock_ttl_ms: 1000\n", "unlock_ttl_ms"},
		{"unknown policy", "agents:\n  - id: ci\n    policy: missing\n", "unknown policy"},
		{"short token", "agents:\n  - id: ci\n    token: short\n", "at least 16"},
		{"oversized agent", "agents:\n  - id: " + strings.Repeat("a", 129) + "\n", "at most 128"},
		{"token whitespace", "agents:\n  - id: ci\n    token: ' 0123456789abcdef'\n", "visible ASCII"},
		{"invalid regex", "redaction:\n  - '[unterminated'\n", "redaction[0]"},
		{"invalid bind", "http:\n  bind: localhost\n", "host:port"},
		{"invalid bind port", "http:\n  bind: 127.0.0.1:70000\n", "1..65535"},
		{"duplicate server", "servers:\n  - {name: prod, host: one, user: ops}\n  - {name: prod, host: two, user: ops}\n", "duplicate server"},
		{"server whitespace", "servers:\n  - {name: 'prod server', host: one, user: ops}\n", "whitespace"},
		{"invalid server port", "servers:\n  - {name: prod, host: one, user: ops, port: 70000}\n", "port"},
		{"oversized output page", "defaults:\n  max_output_bytes: 2000000\n", "output limits"},
		{"oversized timeout", "defaults:\n  timeout_ms: 999999999\n", "timeouts"},
		{"empty recipe step", "recipes:\n  deploy:\n    steps: [[]]\n", "step 0"},
		{"unknown recipe target", "recipes:\n  deploy:\n    target: missing\n    steps: [[true]]\n", "unknown target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadRejectsOversizedConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.Repeat("#", maxConfigBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "config exceeds") {
		t.Fatalf("oversized config error = %v", err)
	}
}
