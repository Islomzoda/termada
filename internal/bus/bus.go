// Package bus is the internal event bus that feeds observability and audit
// (spec §8.7). It is split by guarantee: subscribers here are best-effort
// (bounded queues, drop-oldest) so a slow consumer never blocks producers; the
// audit path writes synchronously and separately (see internal/audit).
package bus

import (
	"sync"
	"time"
)

// Event types.
const (
	EvAgentConnected   = "agent.connected"
	EvSessionCreated   = "session.created"
	EvSessionClosed    = "session.closed"
	EvJobStarted       = "job.started"
	EvJobFinished      = "job.finished"
	EvJobKilled        = "job.killed"
	EvConfirmRequested = "confirm.requested"
	EvConfirmResolved  = "confirm.resolved"
	EvPolicyDenied     = "policy.denied"
	EvFleetStarted     = "fleet.started"
	EvFleetFinished    = "fleet.finished"
)

// Event is a single observable action.
type Event struct {
	Time      time.Time      `json:"time"`
	Type      string         `json:"type"`
	AgentID   string         `json:"agent_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	JobID     string         `json:"job_id,omitempty"`
	Message   string         `json:"message,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// Bus fans events out to subscribers. Publish never blocks.
type Bus struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]chan Event
	// ring keeps the most recent events for late subscribers / the activity feed.
	ring    []Event
	ringCap int
}

// New returns a bus that retains the last ringCap events for replay to new
// subscribers and the dashboard activity feed.
func New(ringCap int) *Bus {
	if ringCap <= 0 {
		ringCap = 500
	}
	return &Bus{subs: map[int]chan Event{}, ringCap: ringCap}
}

// Publish delivers e to all subscribers (best-effort: full queues drop the
// oldest buffered event) and records it in the ring buffer.
func (b *Bus) Publish(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.Lock()
	b.ring = append(b.ring, e)
	if len(b.ring) > b.ringCap {
		b.ring = b.ring[len(b.ring)-b.ringCap:]
	}
	subs := make([]chan Event, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// Slow consumer: drop the oldest queued event to make room (best-effort).
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- e:
			default:
			}
		}
	}
}

// Subscribe returns a channel of future events and a cancel function. The
// channel is buffered; if the subscriber falls behind, oldest events are dropped.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 256
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
}

// Recent returns up to n most-recent events (for the activity feed).
func (b *Bus) Recent(n int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || n > len(b.ring) {
		n = len(b.ring)
	}
	out := make([]Event, n)
	copy(out, b.ring[len(b.ring)-n:])
	return out
}
