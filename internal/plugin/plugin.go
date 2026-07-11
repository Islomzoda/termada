// Package plugin is the out-of-process extension mechanism (spec §29). A plugin
// is an executable in the plugins directory that speaks a tiny JSON protocol:
//
//	<plugin> describe            -> {"tools":[{name,description,inputSchema}]}
//	<plugin> call <tool> <stdin=args-json>  -> result-json
//
// Trust boundary: plugins are trusted local executables, not sandboxed code.
// They run with a minimal environment (so secrets are not inherited by default),
// bounded I/O/time, and best-effort descendant cleanup, but retain the daemon
// user's OS filesystem and network permissions. Install plugins only from trusted sources.
// Plugin tools are surfaced through MCP and the control plane policy/audit gates.
package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	maxPluginInputBytes      = 1 << 20
	maxPluginOutputBytes     = 1 << 20
	maxPluginStderrBytes     = 64 << 10
	maxPluginTools           = 128
	maxPluginNameBytes       = 64
	maxPluginExecutableBytes = 64 << 20
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

	file      os.FileInfo
	digest    [sha256.Size]byte
	toolNames map[string]struct{}
}

// Manager discovers and invokes plugins in a directory.
type Manager struct {
	dir string

	// Per-call subprocess timeouts. Discovery (describe) is bounded shorter than
	// invocation (call). Kept as fields so tests — where a race-instrumented or
	// loaded machine makes process startup slow enough to trip a tight default —
	// can widen them without changing production behavior.
	describeTimeout time.Duration
	callTimeout     time.Duration

	mu        sync.RWMutex
	plugins   map[string]*Plugin
	callSlots chan struct{}
}

// New returns a manager for the given plugins directory.
func New(dir string) *Manager {
	return &Manager{
		dir:             dir,
		plugins:         map[string]*Plugin{},
		callSlots:       make(chan struct{}, 8),
		describeTimeout: 5 * time.Second,
		callTimeout:     60 * time.Second,
	}
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
	duplicates := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(m.dir, e.Name())
		info, err := safePluginFile(path)
		if err != nil {
			continue
		}
		beforeDigest, err := pluginDigest(path, info)
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if !validName(name) || duplicates[name] {
			continue
		}
		if _, exists := loaded[name]; exists {
			delete(loaded, name)
			duplicates[name] = true
			continue
		}
		out, err := run(path, []string{"describe"}, nil, m.describeTimeout)
		if err != nil {
			continue
		}
		var d describeOut
		if json.Unmarshal(out, &d) != nil || len(d.Tools) == 0 || len(d.Tools) > maxPluginTools {
			continue
		}
		toolNames := make(map[string]struct{}, len(d.Tools))
		valid := true
		for i := range d.Tools {
			localName := d.Tools[i].Name
			if !validName(localName) || len(d.Tools[i].Description) > maxPluginOutputBytes || !validInputSchema(d.Tools[i].InputSchema) {
				valid = false
				break
			}
			if _, duplicate := toolNames[localName]; duplicate {
				valid = false
				break
			}
			toolNames[localName] = struct{}{}
			d.Tools[i].Name = name + "." + localName
		}
		if !valid {
			continue
		}
		// Refuse a plugin that was replaced while describe was running. Calls also
		// repeat this identity check, so replacing a loaded executable requires Load.
		current, err := safePluginFile(path)
		if err != nil || !os.SameFile(info, current) {
			continue
		}
		afterDigest, err := pluginDigest(path, current)
		if err != nil || beforeDigest != afterDigest {
			continue
		}
		loaded[name] = &Plugin{Name: name, Path: path, Tools: d.Tools, file: current, digest: afterDigest, toolNames: toolNames}
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
	if _, described := p.toolNames[tool]; !described {
		return nil, fmt.Errorf("plugin %q did not describe tool %q", plug, tool)
	}
	select {
	case m.callSlots <- struct{}{}:
		defer func() { <-m.callSlots }()
	default:
		return nil, fmt.Errorf("plugin process limit reached; retry later")
	}
	current, err := safePluginFile(p.Path)
	if err != nil || !os.SameFile(p.file, current) {
		return nil, fmt.Errorf("plugin %q executable changed since discovery; reload plugins", plug)
	}
	beforeDigest, err := pluginDigest(p.Path, current)
	if err != nil || beforeDigest != p.digest {
		return nil, fmt.Errorf("plugin %q executable changed since discovery; reload plugins", plug)
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode plugin arguments: %w", err)
	}
	if len(payload) > maxPluginInputBytes {
		return nil, fmt.Errorf("plugin arguments exceed %d byte limit", maxPluginInputBytes)
	}
	out, err := run(p.Path, []string{"call", tool}, payload, m.callTimeout)
	if err != nil {
		return nil, err
	}
	current, digestErr := safePluginFile(p.Path)
	if digestErr != nil || !os.SameFile(p.file, current) {
		return nil, fmt.Errorf("plugin %q executable changed during invocation; reload plugins", plug)
	}
	afterDigest, digestErr := pluginDigest(p.Path, current)
	if digestErr != nil || afterDigest != p.digest {
		return nil, fmt.Errorf("plugin %q executable changed during invocation; reload plugins", plug)
	}
	var result any
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("plugin %q returned invalid JSON: %w", plug, err)
	}
	return result, nil
}

func validName(s string) bool {
	if s == "" || len(s) > maxPluginNameBytes {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validInputSchema(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	typ, ok := schema["type"].(string)
	return ok && typ == "object"
}

func safePluginFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("plugin is not a regular executable")
	}
	if runtime.GOOS == "windows" {
		if !strings.EqualFold(filepath.Ext(path), ".exe") {
			return nil, fmt.Errorf("Windows plugins must be .exe files")
		}
		return info, nil // Unix execute/write mode bits do not model Windows ACLs.
	}
	if info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("plugin is not a regular executable")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("plugin is writable by group or others")
	}
	return info, nil
}

func pluginDigest(path string, expected os.FileInfo) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if expected.Size() < 0 || expected.Size() > maxPluginExecutableBytes {
		return digest, fmt.Errorf("plugin executable exceeds %d byte limit", maxPluginExecutableBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return digest, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return digest, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return digest, fmt.Errorf("plugin executable changed while opening")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxPluginExecutableBytes+1))
	if err != nil {
		return digest, err
	}
	if n > maxPluginExecutableBytes {
		return digest, fmt.Errorf("plugin executable exceeds %d byte limit", maxPluginExecutableBytes)
	}
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

type limitedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	stored := 0
	if remaining := b.limit - len(b.data); remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		b.data = append(b.data, p[:remaining]...)
		stored = remaining
	}
	if stored < len(p) {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte  { return b.data }
func (b *limitedBuffer) String() string { return string(b.data) }

// run executes a plugin with a minimal environment (capability boundary: no
// inherited secrets) and an optional stdin payload.
func run(path string, args []string, stdin []byte, timeout time.Duration) ([]byte, error) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	configurePluginCommand(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TERMADA_PLUGIN=1"}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out := &limitedBuffer{limit: maxPluginOutputBytes}
	errOut := &limitedBuffer{limit: maxPluginStderrBytes}
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin %s: %w", filepath.Base(path), err)
	}
	cleanup, err := containPluginProcess(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("contain plugin %s: %w", filepath.Base(path), err)
	}
	err = cmd.Wait()
	cleanup()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("plugin %s timed out after %s", filepath.Base(path), timeout)
	}
	if out.truncated {
		return nil, fmt.Errorf("plugin %s output exceeds %d byte limit", filepath.Base(path), maxPluginOutputBytes)
	}
	if errOut.truncated {
		return nil, fmt.Errorf("plugin %s stderr exceeds %d byte limit", filepath.Base(path), maxPluginStderrBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("plugin %s failed: %v: %s", filepath.Base(path), err, strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

var _ io.Writer = (*limitedBuffer)(nil)
