package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPlugin = `#!/bin/sh
case "$1" in
  describe)
    echo '{"tools":[{"name":"greet","description":"greets someone","inputSchema":{"type":"object","properties":{"who":{"type":"string"}}}}]}'
    ;;
  call)
    # tool name is $2; args arrive as JSON on stdin
    input=$(cat)
    echo "{\"ok\":true,\"tool\":\"$2\",\"echo\":$input}"
    ;;
esac
`

func TestPluginDiscoverAndCall(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "demo")
	if err := os.WriteFile(p, []byte(testPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	// Under `go test -race ./...` the whole binary is race-instrumented and the
	// machine is busy building/running every package in parallel, so spawning the
	// describe subprocess can exceed the tight production default and the plugin
	// would be silently dropped. Give discovery generous headroom for the test.
	m.describeTimeout = 60 * time.Second
	if err := m.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	tools := m.Tools()
	if len(tools) != 1 || tools[0].Name != "demo.greet" {
		t.Fatalf("tools = %+v, want [demo.greet]", tools)
	}

	res, err := m.Call("demo.greet", map[string]any{"who": "world"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	obj, ok := res.(map[string]any)
	if !ok || obj["ok"] != true || obj["tool"] != "greet" {
		t.Fatalf("call result = %+v", res)
	}
	if _, err := m.Call("demo.not-described", nil); err == nil || !strings.Contains(err.Error(), "did not describe") {
		t.Fatalf("undescribed tool call error = %v", err)
	}
	if _, err := m.Call("demo.greet.extra", nil); err == nil {
		t.Fatal("tool suffix was accepted instead of exact described name")
	}
}

func TestPluginNonExecutableIgnored(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "notexec"), []byte(testPlugin), 0o644) // no +x
	m := New(dir)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if len(m.Tools()) != 0 {
		t.Fatalf("non-executable file should be ignored")
	}
}

func TestPluginMissingDir(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "does-not-exist"))
	if err := m.Load(); err != nil {
		t.Fatalf("missing dir should be a no-op, got %v", err)
	}
}

func TestPluginRejectsUnsafeOrInvalidDescriptions(t *testing.T) {
	dir := t.TempDir()
	plugins := map[string]string{
		"bad-name": `#!/bin/sh
echo '{"tools":[{"name":"bad.name","inputSchema":{"type":"object"}}]}'
`,
		"duplicate": `#!/bin/sh
echo '{"tools":[{"name":"same","inputSchema":{"type":"object"}},{"name":"same","inputSchema":{"type":"object"}}]}'
`,
		"bad-schema": `#!/bin/sh
echo '{"tools":[{"name":"tool","inputSchema":{"type":"string"}}]}'
`,
	}
	for name, body := range plugins {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink and a writable-by-others executable are not trusted plugin files.
	if err := os.Symlink(filepath.Join(dir, "bad-schema"), filepath.Join(dir, "linked")); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "writable"), []byte(testPlugin), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "writable"), 0o777); err != nil {
		t.Fatal(err)
	}

	m := New(dir)
	m.describeTimeout = 30 * time.Second
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if got := m.Tools(); len(got) != 0 {
		t.Fatalf("invalid plugins exposed tools: %+v", got)
	}
}

func TestPluginExecutableReplacementRequiresReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo")
	if err := os.WriteFile(path, []byte(testPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	m.describeTimeout = 30 * time.Second
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(testPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("demo.greet", nil); err == nil || !strings.Contains(err.Error(), "changed since discovery") {
		t.Fatalf("replaced executable call error = %v", err)
	}
}

func TestPluginInPlaceModificationRequiresReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo")
	if err := os.WriteFile(path, []byte(testPlugin), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	m.describeTimeout = 30 * time.Second
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(testPlugin + "\n# modified in place\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("demo.greet", nil); err == nil || !strings.Contains(err.Error(), "changed since discovery") {
		t.Fatalf("in-place modified executable call error = %v", err)
	}
}

func TestPluginIOIsBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noisy")
	script := `#!/bin/sh
case "$1" in
  describe) echo '{"tools":[{"name":"noise","inputSchema":{"type":"object"}}]}' ;;
  call) head -c 1100000 /dev/zero ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	m.describeTimeout = 30 * time.Second
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("noisy.noise", nil); err == nil || !strings.Contains(err.Error(), "output exceeds") {
		t.Fatalf("oversized output error = %v", err)
	}
	if _, err := m.Call("noisy.noise", map[string]any{"data": strings.Repeat("x", maxPluginInputBytes)}); err == nil || !strings.Contains(err.Error(), "arguments exceed") {
		t.Fatalf("oversized input error = %v", err)
	}
}
