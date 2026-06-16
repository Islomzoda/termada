package plugin

import (
	"os"
	"path/filepath"
	"testing"
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
