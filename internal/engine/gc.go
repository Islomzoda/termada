package engine

import (
	"sort"
	"time"
)

// GCOnce prunes the job registry (spec EX-9): terminal jobs that finished longer
// than maxAgeMS ago are dropped, and the number of retained terminal jobs is
// capped at maxKeep (oldest evicted first). Active and backgrounded jobs are
// never pruned. Recovered (orphaned) entries live in m.recovered and are left
// alone here.
func (m *Manager) GCOnce(maxAgeMS, maxKeep int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	type ent struct {
		id    string
		ended int64
	}
	now := time.Now().UnixMilli()
	var term []ent
	for id, j := range m.jobs {
		j.mu.Lock()
		terminal := j.status.Terminal()
		ended := j.endedAt.UnixMilli()
		j.mu.Unlock()
		if terminal {
			term = append(term, ent{id, ended})
		}
	}
	if maxAgeMS > 0 {
		for _, e := range term {
			if now-e.ended > int64(maxAgeMS) {
				delete(m.jobs, e.id)
			}
		}
	}
	if maxKeep > 0 {
		remaining := term[:0]
		for _, e := range term {
			if _, ok := m.jobs[e.id]; ok {
				remaining = append(remaining, e)
			}
		}
		if len(remaining) > maxKeep {
			sort.Slice(remaining, func(i, j int) bool { return remaining[i].ended < remaining[j].ended })
			for _, e := range remaining[:len(remaining)-maxKeep] {
				delete(m.jobs, e.id)
			}
		}
	}
}
