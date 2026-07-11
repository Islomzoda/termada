package engine

import (
	"sort"
	"strings"
	"time"

	"github.com/termada/termada/internal/bus"
)

const maxTrackedAgents = 1024

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
	// LastCommandUnix anchors LastCommand to when that command actually started,
	// distinct from LastSeenUnix (which any activity — connect, session, deny —
	// bumps), so the dashboard doesn't show a stale command as "just now".
	LastCommandUnix int64    `json:"last_command_unix,omitempty"`
	History         []string `json:"history,omitempty"` // recent commands, newest last
}

// recordCommand sets the last command (with its timestamp) and appends it to the
// capped history.
func (a *AgentStat) recordCommand(cmd string) {
	a.LastCommand = cmd
	a.LastCommandUnix = time.Now().Unix()
	a.History = append(a.History, cmd)
	const max = 12
	if len(a.History) > max {
		a.History = a.History[len(a.History)-max:]
	}
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
		if len(m.agents) >= maxTrackedAgents {
			oldestID := ""
			var oldestSeen int64
			for candidateID, candidate := range m.agents {
				if oldestID == "" || candidate.LastSeenUnix < oldestSeen {
					oldestID = candidateID
					oldestSeen = candidate.LastSeenUnix
				}
			}
			delete(m.agents, oldestID)
		}
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
		c := *a
		// Deep-copy History: a plain struct copy shares the slice backing array,
		// which recordCommand mutates under the lock — the caller reads the
		// snapshot lock-free (JSON-encodes it), so a shared array is a data race.
		c.History = append([]string(nil), a.History...)
		out = append(out, c)
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
