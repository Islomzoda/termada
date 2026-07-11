// Package fleet runs a command across a group of servers and aggregates the
// results (spec §15/FL-1..FL-3). Execution is delegated to a Runner, so the
// selection/aggregation logic is unit-testable independently of real SSH.
package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/termada/termada/internal/errs"
)

const (
	maxFleetCommandArgs  = 4096
	maxFleetCommandBytes = 256 << 10
	maxFleetTargets      = 256
	maxFleetSelectors    = 512
	maxFleetResultBytes  = 2 << 20
	maxServerInventory   = 1024
)

const fleetOutputTruncatedMarker = "\n[termada: fleet output truncated]\n"

// Server is a configured remote host. Auth references a vault entry, never a
// secret value. Managed servers were added at runtime (via the dashboard) and
// are persisted/removable; non-managed servers come from config.yaml.
type Server struct {
	Name    string   `json:"name"`
	Host    string   `json:"host"`
	Port    int      `json:"port,omitempty"`
	User    string   `json:"user"`
	Auth    string   `json:"auth"`
	Tags    []string `json:"tags,omitempty"`
	Managed bool     `json:"-"`
}

// ServerInfo is the secret-free view returned to agents (spec server_list).
type ServerInfo struct {
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	User        string   `json:"user"`
	Tags        []string `json:"tags,omitempty"`
	Managed     bool     `json:"managed"`
	Status      string   `json:"status,omitempty"`       // last health-check state
	CheckedUnix int64    `json:"checked_unix,omitempty"` // when last checked
}

// Result is one server's outcome.
type Result struct {
	Server     string `json:"server"`
	Status     string `json:"status"` // ok | nonzero_exit | unreachable | timeout | conn_lost | denied
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// RunResult is the aggregate (spec FL-1 schema). fleet_run is NOT atomic.
type RunResult struct {
	Status  string         `json:"status"` // ok | partial | failed
	Results []Result       `json:"results"`
	Summary map[string]int `json:"summary"`
}

// Runner executes a command on a single server.
type Runner interface {
	Run(server Server, command []string) Result
}

// Manager owns the server inventory and a runner.
type Manager struct {
	mu          sync.RWMutex
	servers     []Server
	runner      Runner
	parallelism int
	runSlots    chan struct{}     // manager-wide SSH execution ceiling
	storePath   string            // where dashboard-added (managed) servers persist
	status      map[string]string // server name -> last health-check state
	checked     map[string]int64  // server name -> unix time of last check
}

// New builds a fleet manager.
func New(servers []Server, runner Runner, parallelism int) *Manager {
	if parallelism <= 0 {
		parallelism = 5
	}
	return &Manager{servers: servers, runner: runner, parallelism: parallelism, runSlots: make(chan struct{}, parallelism),
		status: map[string]string{}, checked: map[string]int64{}}
}

// HealthCheck tests every server (runs `true` over SSH) and caches the per-server
// status so the dashboard can show online/offline without the human clicking. A
// no-op when there are no servers.
func (m *Manager) HealthCheck() {
	res, err := m.Run([]string{"true"}, nil, m.parallelism)
	if err != nil || res == nil {
		return
	}
	now := time.Now().Unix()
	m.mu.Lock()
	for _, r := range res.Results {
		m.status[r.Server] = r.Status
		m.checked[r.Server] = now
	}
	m.mu.Unlock()
}

// SetServers replaces the inventory (hot-reload).
func (m *Manager) SetServers(servers []Server) {
	m.mu.Lock()
	m.servers = servers
	m.mu.Unlock()
}

// ServerList returns the secret-free inventory.
func (m *Manager) ServerList() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerInfo, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, ServerInfo{Name: s.Name, Host: s.Host, User: s.User, Tags: s.Tags,
			Managed: s.Managed, Status: m.status[s.Name], CheckedUnix: m.checked[s.Name]})
	}
	return out
}

// Get returns the full server record (including the vault Auth reference) by
// name. Used to open a remote session.
func (m *Manager) Get(name string) (Server, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.servers {
		if s.Name == name {
			return s, true
		}
	}
	return Server{}, false
}

// LoadStore loads previously dashboard-added servers from path and merges them
// into the inventory.
func (m *Manager) LoadStore(path string) {
	m.mu.Lock()
	m.storePath = path
	m.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var managed []Server
	if json.Unmarshal(data, &managed) != nil {
		return
	}
	m.mu.Lock()
	seen := make(map[string]bool, len(m.servers)+len(managed))
	for _, server := range m.servers {
		seen[server.Name] = true
	}
	for i := range managed {
		if len(m.servers) >= maxServerInventory || validateServer(managed[i]) != nil || seen[managed[i].Name] {
			continue
		}
		managed[i].Managed = true
		m.servers = append(m.servers, managed[i])
		seen[managed[i].Name] = true
	}
	m.mu.Unlock()
}

// AddServer adds a runtime (managed) server and persists it. Errors if the name
// is already taken.
func (m *Manager) AddServer(s Server) error {
	if err := validateServer(s); err != nil {
		return err
	}
	s.Managed = true
	m.mu.Lock()
	if len(m.servers) >= maxServerInventory {
		m.mu.Unlock()
		return errs.New(errs.ParallelismExceeded, "server inventory reached its limit (%d)", maxServerInventory)
	}
	for _, x := range m.servers {
		if x.Name == s.Name {
			m.mu.Unlock()
			return errs.New(errs.InvalidArgument, "server %q already exists", s.Name)
		}
	}
	m.servers = append(m.servers, s)
	err := m.saveLocked()
	m.mu.Unlock()
	return err
}

func validateServer(s Server) error {
	validField := func(value string, max int) bool {
		return value != "" && len(value) <= max && strings.TrimSpace(value) == value &&
			strings.IndexFunc(value, func(r rune) bool { return r < 0x21 || r == 0x7f }) < 0
	}
	if !validField(s.Name, 255) || !validField(s.Host, 1024) || !validField(s.User, 255) {
		return errs.New(errs.InvalidArgument, "server requires bounded visible name, host and user fields")
	}
	if s.Port < 0 || s.Port > 65535 {
		return errs.New(errs.InvalidArgument, "server port must be 0 or in 1..65535")
	}
	if len(s.Auth) > 1024 || strings.IndexFunc(s.Auth, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errs.New(errs.InvalidArgument, "server auth reference is invalid")
	}
	if len(s.Tags) > 64 {
		return errs.New(errs.InvalidArgument, "server has too many tags")
	}
	for _, tag := range s.Tags {
		if !validField(tag, 128) {
			return errs.New(errs.InvalidArgument, "server tag is invalid")
		}
	}
	return nil
}

// RemoveServer removes a managed server (config-defined servers cannot be
// removed via the API).
func (m *Manager) RemoveServer(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var kept []Server
	found := false
	for _, s := range m.servers {
		if s.Name == name {
			if !s.Managed {
				return errs.New(errs.DeniedByPolicy, "server %q is defined in config.yaml; edit the file to remove it", name)
			}
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		return errs.New(errs.NotFound, "server %q not found or not managed", name)
	}
	m.servers = kept
	return m.saveLocked()
}

// saveLocked persists the managed servers. Caller holds m.mu.
func (m *Manager) saveLocked() error {
	if m.storePath == "" {
		return nil
	}
	var managed []Server
	for _, s := range m.servers {
		if s.Managed {
			managed = append(managed, s)
		}
	}
	data, err := json.MarshalIndent(managed, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.storePath), 0o700); err != nil {
		return err
	}
	tmp := m.storePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.storePath)
}

// selectServers resolves a selector (server names and/or tags) to servers. An
// empty selector means all servers.
func (m *Manager) selectServers(selector []string) []Server {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(selector) == 0 {
		return append([]Server(nil), m.servers...)
	}
	want := map[string]bool{}
	for _, s := range selector {
		want[s] = true
	}
	seen := map[string]bool{}
	var out []Server
	for _, s := range m.servers {
		if seen[s.Name] {
			continue
		}
		if want[s.Name] {
			out = append(out, s)
			seen[s.Name] = true
			continue
		}
		for _, t := range s.Tags {
			if want[t] {
				out = append(out, s)
				seen[s.Name] = true
				break
			}
		}
	}
	return out
}

// Run executes command across the selected servers concurrently (bounded by
// parallelism) and aggregates the results.
func (m *Manager) Run(command []string, selector []string, parallelism int) (*RunResult, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, errs.New(errs.InvalidArgument, "fleet command must not be empty")
	}
	if len(command) > maxFleetCommandArgs {
		return nil, errs.New(errs.InvalidArgument, "fleet command has too many arguments")
	}
	totalCommandBytes := 0
	for _, arg := range command {
		if strings.IndexByte(arg, 0) >= 0 {
			return nil, errs.New(errs.InvalidArgument, "fleet command arguments must not contain NUL bytes")
		}
		if len(arg) > maxFleetCommandBytes-totalCommandBytes {
			return nil, errs.New(errs.InvalidArgument, "fleet command exceeds %d byte argv limit", maxFleetCommandBytes)
		}
		totalCommandBytes += len(arg)
	}
	if len(selector) > maxFleetSelectors {
		return nil, errs.New(errs.InvalidArgument, "fleet selector has too many entries")
	}
	for _, item := range selector {
		if item == "" || len(item) > 255 || strings.IndexFunc(item, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return nil, errs.New(errs.InvalidArgument, "fleet selector contains an invalid entry")
		}
	}
	servers := m.selectServers(selector)
	if len(servers) == 0 {
		return nil, errs.New(errs.NotFound, "no servers matched selector")
	}
	if len(servers) > maxFleetTargets {
		return nil, errs.New(errs.ParallelismExceeded, "fleet run matched %d servers; limit is %d", len(servers), maxFleetTargets)
	}
	// The request may ask for less concurrency, but never more than the manager's
	// configured ceiling. Besides protecting remote hosts, this prevents an
	// attacker-controlled integer from becoming an enormous channel allocation.
	if parallelism <= 0 || parallelism > m.parallelism {
		parallelism = m.parallelism
	}
	if parallelism > len(servers) {
		parallelism = len(servers)
	}

	results := make([]Result, len(servers))
	requestSlots := make(chan struct{}, parallelism)
	remainingOutput := maxFleetResultBytes
	var outputMu sync.Mutex
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		requestSlots <- struct{}{}
		m.runSlots <- struct{}{}
		go func(i int, srv Server) {
			defer wg.Done()
			defer func() { <-m.runSlots }()
			defer func() { <-requestSlots }()
			result := m.runner.Run(srv, command)
			outputMu.Lock()
			boundResultOutput(&result, &remainingOutput)
			outputMu.Unlock()
			results[i] = result
		}(i, srv)
	}
	wg.Wait()

	summary := map[string]int{}
	ok := 0
	for _, r := range results {
		summary[r.Status]++
		if r.Status == "ok" {
			ok++
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Server < results[j].Server })

	status := "ok"
	switch {
	case ok == 0:
		status = "failed"
	case ok < len(results):
		status = "partial"
	}
	return &RunResult{Status: status, Results: results, Summary: summary}, nil
}

// BoundRunOutput reapplies the aggregate output bound after a caller transforms
// result strings (for example, redaction can change their encoded size).
func BoundRunOutput(result *RunResult) {
	if result == nil {
		return
	}
	remaining := maxFleetResultBytes
	for i := range result.Results {
		boundResultOutput(&result.Results[i], &remaining)
	}
}

func boundResultOutput(result *Result, remaining *int) {
	result.Stdout = takeFleetOutput(result.Stdout, remaining, &result.Truncated)
	result.Stderr = takeFleetOutput(result.Stderr, remaining, &result.Truncated)
	result.Error = takeFleetOutput(result.Error, remaining, &result.Truncated)
}

func takeFleetOutput(value string, remaining *int, truncated *bool) string {
	if value == "" {
		return ""
	}
	if *remaining <= 0 {
		*truncated = true
		return ""
	}
	if len(value) <= *remaining {
		*remaining -= len(value)
		return value
	}
	*truncated = true
	budget := *remaining
	*remaining = 0
	if budget <= len(fleetOutputTruncatedMarker) {
		return value[:budget]
	}
	return value[:budget-len(fleetOutputTruncatedMarker)] + fleetOutputTruncatedMarker
}
