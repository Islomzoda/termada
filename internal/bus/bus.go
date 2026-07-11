// Package bus is the internal event bus that feeds observability and audit
// (spec §8.7). It is split by guarantee: subscribers here are best-effort
// (bounded queues, drop-oldest) so a slow consumer never blocks producers; the
// audit path writes synchronously and separately (see internal/audit).
package bus

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const maxEventBytes = 512 << 10

// Event types.
const (
	EvAgentConnected    = "agent.connected"
	EvSessionCreated    = "session.created"
	EvSessionClosed     = "session.closed"
	EvSessionReset      = "session.reset" // remote link dropped & reconnected; cwd/env lost
	EvJobStartRequested = "job.start_requested"
	EvJobStarted        = "job.started"
	EvJobFinished       = "job.finished"
	EvJobKilled         = "job.killed"
	EvConfirmRequested  = "confirm.requested"
	EvConfirmResolved   = "confirm.resolved"
	EvPolicyDenied      = "policy.denied"
	EvFleetStarted      = "fleet.started"
	EvFleetFinished     = "fleet.finished"
	EvPluginStarted     = "plugin.started"
	EvPluginFinished    = "plugin.finished"
	EvPersistenceError  = "persistence.error"
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

// Bus fans events out to subscribers. Best-effort subscribers never block
// publishers. Reliable subscribers run synchronously and may apply backpressure.
type Bus struct {
	// publishMu gives reliable sinks and the in-memory feed one total event
	// order. It also lets cancellation wait for an in-flight reliable delivery.
	publishMu sync.Mutex
	mu        sync.Mutex
	nextID    int
	subs      map[int]chan Event
	reliable  map[int]func(Event) error
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
	return &Bus{
		subs:     map[int]chan Event{},
		reliable: map[int]func(Event) error{},
		ringCap:  ringCap,
	}
}

// Publish first delivers e to every reliable sink, then records it in the ring
// and fans it out to best-effort subscribers. Reliable sink errors are returned;
// observability delivery still proceeds so the failure itself remains visible.
func (b *Bus) Publish(e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	encoded, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	if len(encoded) > maxEventBytes {
		return fmt.Errorf("event is %d bytes, exceeds %d byte limit", len(encoded), maxEventBytes)
	}
	b.publishMu.Lock()
	defer b.publishMu.Unlock()

	b.mu.Lock()
	reliable := make([]func(Event) error, 0, len(b.reliable))
	for _, sink := range b.reliable {
		reliable = append(reliable, sink)
	}
	b.mu.Unlock()

	var deliveryErr error
	for _, sink := range reliable {
		if err := sink(e); err != nil {
			deliveryErr = errors.Join(deliveryErr, fmt.Errorf("reliable event delivery: %w", err))
		}
	}

	b.mu.Lock()
	b.ring = append(b.ring, e)
	if len(b.ring) > b.ringCap {
		b.ring = b.ring[len(b.ring)-b.ringCap:]
	}
	for _, ch := range b.subs {
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
	b.mu.Unlock()
	return deliveryErr
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

// SubscribeReliable registers a synchronous event sink. Unlike Subscribe, its
// events are never dropped: Publish does not return until handler has returned.
// The returned cancel function waits for any in-flight handler call to finish.
// A reliable handler must not call its own cancel function.
func (b *Bus) SubscribeReliable(handler func(Event) error) func() {
	if handler == nil {
		return func() {}
	}
	b.publishMu.Lock()
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.reliable[id] = handler
	b.mu.Unlock()
	b.publishMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.publishMu.Lock()
			b.mu.Lock()
			delete(b.reliable, id)
			b.mu.Unlock()
			b.publishMu.Unlock()
		})
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
