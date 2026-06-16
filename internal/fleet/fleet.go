// Package fleet runs a command across a group of servers and aggregates the
// results (spec §15/FL-1..FL-3). Execution is delegated to a Runner, so the
// selection/aggregation logic is unit-testable independently of real SSH.
package fleet

import (
	"sort"
	"sync"

	"github.com/termada/termada/internal/errs"
)

// Server is a configured remote host. Auth references a vault entry, never a
// secret value.
type Server struct {
	Name string
	Host string
	Port int
	User string
	Auth string
	Tags []string
}

// ServerInfo is the secret-free view returned to agents (spec server_list).
type ServerInfo struct {
	Name string   `json:"name"`
	Host string   `json:"host"`
	User string   `json:"user"`
	Tags []string `json:"tags,omitempty"`
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
}

// New builds a fleet manager.
func New(servers []Server, runner Runner, parallelism int) *Manager {
	if parallelism <= 0 {
		parallelism = 5
	}
	return &Manager{servers: servers, runner: runner, parallelism: parallelism}
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
		out = append(out, ServerInfo{Name: s.Name, Host: s.Host, User: s.User, Tags: s.Tags})
	}
	return out
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
	servers := m.selectServers(selector)
	if len(servers) == 0 {
		return nil, errs.New(errs.NotFound, "no servers matched selector")
	}
	if parallelism <= 0 {
		parallelism = m.parallelism
	}

	results := make([]Result, len(servers))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, srv Server) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = m.runner.Run(srv, command)
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
