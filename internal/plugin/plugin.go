// Package plugin is the out-of-process extension mechanism (spec §29). A plugin
// is an executable in the plugins directory that speaks a tiny JSON protocol:
//
//	<plugin> describe            -> {"tools":[{name,description,inputSchema}]}
//	<plugin> call <tool> <stdin=args-json>  -> result-json
//
// Capability boundary (spec §29/§3a): plugins run as separate processes with a
// minimal environment — they get the call arguments and nothing else (no vault,
// no audit key, no dashboard token, no inherited secrets). Plugin tools are
// surfaced to agents through MCP, and the control-plane dispatch gates each call
// through the acting agent's policy and records it in the audit log (modelled as
// the command ["plugin", <name>]); a call that policy flags for confirmation is
// refused (fail-closed), since human approval isn't wired for plugins.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ToolSpec describes a tool a plugin provides. The Name is namespaced as
// "<plugin>.<tool>" once loaded.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type describeOut struct {
	Tools []ToolSpec `json:"tools"`
}

// Plugin is a loaded plugin executable.
type Plugin struct {
	Name  string
	Path  string
	Tools []ToolSpec
}

// Manager discovers and invokes plugins in a directory.
type Manager struct {
	dir string

	mu      sync.RWMutex
	plugins map[string]*Plugin
}

// New returns a manager for the given plugins directory.
func New(dir string) *Manager {
	return &Manager{dir: dir, plugins: map[string]*Plugin{}}
}

// Load (re)scans the plugins directory, querying each executable for its tools.
func (m *Manager) Load() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	loaded := map[string]*Plugin{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Mode()&0o111 == 0 {
			continue // not executable
		}
		path := filepath.Join(m.dir, e.Name())
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		out, err := run(path, []string{"describe"}, nil, 5*time.Second)
		if err != nil {
			continue
		}
		var d describeOut
		if json.Unmarshal(out, &d) != nil {
			continue
		}
		for i := range d.Tools {
			d.Tools[i].Name = name + "." + d.Tools[i].Name
		}
		loaded[name] = &Plugin{Name: name, Path: path, Tools: d.Tools}
	}
	m.mu.Lock()
	m.plugins = loaded
	m.mu.Unlock()
	return nil
}

// Tools returns all plugin tools (namespaced).
func (m *Manager) Tools() []ToolSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []ToolSpec{}
	for _, p := range m.plugins {
		out = append(out, p.Tools...)
	}
	return out
}

// List returns the loaded plugins.
func (m *Manager) List() []*Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		out = append(out, p)
	}
	return out
}

// Call invokes a namespaced tool ("<plugin>.<tool>") with args.
func (m *Manager) Call(name string, args map[string]any) (any, error) {
	plug, tool, ok := strings.Cut(name, ".")
	if !ok {
		return nil, fmt.Errorf("invalid plugin tool name %q", name)
	}
	m.mu.RLock()
	p := m.plugins[plug]
	m.mu.RUnlock()
	if p == nil {
		return nil, fmt.Errorf("no such plugin %q", plug)
	}
	payload, _ := json.Marshal(args)
	out, err := run(p.Path, []string{"call", tool}, payload, 60*time.Second)
	if err != nil {
		return nil, err
	}
	var result any
	if json.Unmarshal(out, &result) != nil {
		return string(out), nil
	}
	return result, nil
}

// run executes a plugin with a minimal environment (capability boundary: no
// inherited secrets) and an optional stdin payload.
func run(path string, args []string, stdin []byte, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TERMADA_PLUGIN=1"}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("plugin %s failed: %v: %s", filepath.Base(path), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}
