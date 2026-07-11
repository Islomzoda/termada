package engine

import (
	"fmt"
	"testing"
)

func TestAgentStatsRegistryIsBounded(t *testing.T) {
	m := NewManager(DefaultConfig())
	for i := 0; i < maxTrackedAgents+100; i++ {
		m.RecordConnect(fmt.Sprintf("agent-%d", i))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.agents) != maxTrackedAgents {
		t.Fatalf("tracked %d agents, want cap %d", len(m.agents), maxTrackedAgents)
	}
	if m.agents[fmt.Sprintf("agent-%d", maxTrackedAgents+99)] == nil {
		t.Fatal("most recently added agent was not retained")
	}
}
