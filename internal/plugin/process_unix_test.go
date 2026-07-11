//go:build !windows

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPluginTimeoutKillsDescendants(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
  describe) echo '{"tools":[{"name":"hang","inputSchema":{"type":"object"}}]}' ;;
  call)
    sleep 30 &
    echo $! > %q
    wait
    ;;
esac
`, pidPath)
	path := filepath.Join(dir, "tree")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	m.describeTimeout = 30 * time.Second
	m.callTimeout = 100 * time.Millisecond
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call("tree.hang", nil); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("plugin child process %d survived timeout (kill probe: %v)", pid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
