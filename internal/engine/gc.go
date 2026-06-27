package engine

import (
	"sort"
	"time"

	"github.com/termada/termada/internal/bus"
)

// ReapOnce SIGKILLs jobs that have been running longer than Config.MaxJobRuntimeMS
// — a safety net for runaway/hung jobs that never emit a completion marker and
// would otherwise pin their session and the global foreground-job quota forever.
// DISABLED by default (0): a long-lived dev server is a first-class use case, so
// reaping only happens when an operator opts in (typically CI/headless). Parked
// confirm-jobs are left to their own ConfirmTimeout. Returns the number reaped.
func (m *Manager) ReapOnce() int {
	maxMS := m.cfg.MaxJobRuntimeMS
	if maxMS <= 0 {
		return 0
	}
	cutoff := time.Duration(maxMS) * time.Millisecond
	now := time.Now()
	var victims []string
	m.mu.Lock()
	for id, j := range m.jobs {
		j.mu.Lock()
		terminal := j.status.Terminal()
		parked := j.status == StatusAwaitingConfirmation
		started := j.startedAt
		j.mu.Unlock()
		if terminal || parked || started.IsZero() {
			continue
		}
		if now.Sub(started) > cutoff {
			victims = append(victims, id)
		}
	}
	m.mu.Unlock()
	for _, id := range victims {
		m.publish(bus.Event{Type: bus.EvJobKilled, JobID: id,
			Message: "reaped: exceeded max_job_runtime_ms"})
		_ = m.Kill(id)
	}
	return len(victims)
}

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
