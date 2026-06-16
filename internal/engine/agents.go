package engine

import (
	"sort"
	"strings"
	"time"

	"github.com/termada/termada/internal/bus"
)

// AgentStat is per-agent activity surfaced to the human (spec MA-2): who
// connected, how often, and what they did.
type AgentStat struct {
	ID            string `json:"id"`
	Connections   int    `json:"connections"`
	Jobs          int    `json:"jobs"`
	Sessions      int    `json:"sessions"`
	Denied        int    `json:"denied"`
	FirstSeenUnix int64  `json:"first_seen_unix"`
	LastSeenUnix  int64  `json:"last_seen_unix"`
	LastCommand   string `json:"last_command,omitempty"`
}

// touchAgent gets-or-creates an agent's stats, stamps last-seen, and applies
// the mutation. Caller must NOT hold m.mu.
func (m *Manager) touchAgent(id string, mutate func(*AgentStat)) {
	if id == "" {
		id = "default"
	}
	now := time.Now().Unix()
	m.mu.Lock()
	a := m.agents[id]
	if a == nil {
		a = &AgentStat{ID: id, FirstSeenUnix: now}
		m.agents[id] = a
	}
	a.LastSeenUnix = now
	if mutate != nil {
		mutate(a)
	}
	m.mu.Unlock()
}

// RecordConnect counts a new agent connection (one per MCP client launch).
func (m *Manager) RecordConnect(id string) {
	m.touchAgent(id, func(a *AgentStat) { a.Connections++ })
	m.publish(bus.Event{Type: bus.EvAgentConnected, AgentID: id, Message: "agent connected"})
}

// Agents returns a snapshot of all known agents, most-recently-active first.
func (m *Manager) Agents() []AgentStat {
	m.mu.Lock()
	out := make([]AgentStat, 0, len(m.agents))
	for _, a := range m.agents {
		out = append(out, *a)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenUnix > out[j].LastSeenUnix })
	return out
}

func cmdString(argv []string) string {
	s := strings.Join(argv, " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
