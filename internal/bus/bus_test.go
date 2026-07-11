package bus

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPublishConcurrentWithCancel(t *testing.T) {
	b := New(64)
	const publishers = 8
	const eventsPerPublisher = 4000

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < eventsPerPublisher; j++ {
				_ = b.Publish(Event{Type: EvJobStarted})
			}
		}()
	}
	close(start)
	for i := 0; i < 4000; i++ {
		_, cancel := b.Subscribe(1)
		cancel()
		cancel() // cancellation is idempotent
	}
	wg.Wait()
}

func TestOversizedEventIsRejectedBeforeDeliveryOrRetention(t *testing.T) {
	b := New(4)
	delivered := false
	b.SubscribeReliable(func(Event) error { delivered = true; return nil })
	err := b.Publish(Event{Type: EvJobStarted, Message: strings.Repeat("x", maxEventBytes+1)})
	if err == nil {
		t.Fatal("oversized event was accepted")
	}
	if delivered || len(b.Recent(10)) != 0 {
		t.Fatal("oversized event reached a sink or the in-memory ring")
	}
}

func TestReliableSubscriberNeverDropsAndSurfacesErrors(t *testing.T) {
	b := New(4)
	wantErr := errors.New("disk unavailable")
	var delivered atomic.Int64
	cancel := b.SubscribeReliable(func(e Event) error {
		n := delivered.Add(1)
		if n == 7 {
			return wantErr
		}
		return nil
	})

	for i := 0; i < 20; i++ {
		err := b.Publish(Event{Type: EvJobStarted})
		if i == 6 {
			if !errors.Is(err, wantErr) {
				t.Fatalf("Publish error = %v, want %v", err, wantErr)
			}
		} else if err != nil {
			t.Fatalf("Publish %d: unexpected error: %v", i, err)
		}
	}
	if got := delivered.Load(); got != 20 {
		t.Fatalf("reliable deliveries = %d, want 20", got)
	}
	cancel()
	_ = b.Publish(Event{Type: EvJobFinished})
	if got := delivered.Load(); got != 20 {
		t.Fatalf("delivery after cancel: got %d calls, want 20", got)
	}
}
