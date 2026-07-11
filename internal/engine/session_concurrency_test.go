package engine

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/termada/termada/internal/bus"
)

type blockingShell struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type reconnectWhileWritingShell struct {
	writeStarted     chan struct{}
	releaseWrite     chan struct{}
	reconnectStarted chan struct{}
	releaseReconnect chan struct{}
	writeOnce        sync.Once
	reconnectOnce    sync.Once
}

type scriptedRead struct {
	data []byte
	err  error
}

// earlyReconnectShell drops the initial transport before the init marker. The
// reconnect therefore races session construction at exactly the old onReset
// assignment boundary; later writes complete normally.
type earlyReconnectShell struct {
	reads      chan scriptedRead
	closed     chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	writes     int
	reconnects int
}

func newEarlyReconnectShell() *earlyReconnectShell {
	return &earlyReconnectShell{reads: make(chan scriptedRead, 8), closed: make(chan struct{})}
}

func (s *earlyReconnectShell) Read(p []byte) (int, error) {
	select {
	case result := <-s.reads:
		return copy(p, result.data), result.err
	case <-s.closed:
		return 0, io.EOF
	}
}

func (s *earlyReconnectShell) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.writes++
	first := s.writes == 1
	s.mu.Unlock()
	if first {
		s.reads <- scriptedRead{err: io.EOF}
		return len(p), nil
	}
	start := bytes.Index(p, []byte("TERMADA:"))
	if start >= 0 {
		start += len("TERMADA:")
		if end := bytes.Index(p[start:], []byte(":%d")); end >= 0 {
			marker := string(p[start : start+end])
			s.reads <- scriptedRead{data: []byte("\x1eTERMADA:" + marker + ":0\x1e")}
		}
	}
	return len(p), nil
}

func (s *earlyReconnectShell) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}
func (s *earlyReconnectShell) Signal(string) error { return nil }
func (s *earlyReconnectShell) Reconnect() error {
	s.mu.Lock()
	s.reconnects++
	s.mu.Unlock()
	return nil
}

func newReconnectWhileWritingShell() *reconnectWhileWritingShell {
	return &reconnectWhileWritingShell{
		writeStarted: make(chan struct{}), releaseWrite: make(chan struct{}),
		reconnectStarted: make(chan struct{}), releaseReconnect: make(chan struct{}),
	}
}

func (s *reconnectWhileWritingShell) Read([]byte) (int, error) { return 0, io.EOF }
func (s *reconnectWhileWritingShell) Write(p []byte) (int, error) {
	s.writeOnce.Do(func() { close(s.writeStarted) })
	<-s.releaseWrite
	return len(p), nil
}
func (s *reconnectWhileWritingShell) Reconnect() error {
	s.reconnectOnce.Do(func() { close(s.reconnectStarted) })
	<-s.releaseReconnect
	return errors.New("test reconnect stopped")
}
func (s *reconnectWhileWritingShell) Close() error {
	select {
	case <-s.releaseWrite:
	default:
		close(s.releaseWrite)
	}
	return nil
}
func (s *reconnectWhileWritingShell) Signal(string) error { return nil }

func newBlockingShell() *blockingShell {
	return &blockingShell{started: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingShell) Read([]byte) (int, error) { return 0, io.EOF }
func (s *blockingShell) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return 0, errors.New("closed")
}
func (s *blockingShell) Close() error {
	select {
	case <-s.release:
	default:
		close(s.release)
	}
	return nil
}
func (s *blockingShell) Signal(string) error { return nil }

func TestBlockedInputWriteDoesNotHoldSessionStateMutex(t *testing.T) {
	shell := newBlockingShell()
	s := &Session{ID: "session", shell: shell, cfg: SessionConfig{OutputRetentionBytes: 1024}}
	job := newJob(s, []string{"read"}, ModeForeground)
	job.activate()
	s.current = job
	done := make(chan error, 1)
	go func() { done <- s.writeInput(job, []byte("input")) }()
	<-shell.started
	stateRead := make(chan struct{})
	go func() { _ = s.currentJob(); close(stateRead) }()
	select {
	case <-stateRead:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("blocked PTY write held the session state mutex")
	}
	_ = shell.Close()
	<-done
}

func TestCloseCannotReactivateJobDuringCommandWrite(t *testing.T) {
	shell := newBlockingShell()
	s := &Session{ID: "session", shell: shell, cfg: SessionConfig{OutputRetentionBytes: 1024}}
	job := newJob(s, []string{"true"}, ModeForeground)
	done := make(chan error, 1)
	go func() { done <- s.startJob(job, "true") }()
	<-shell.started
	s.close()
	<-done
	if status := job.info().Status; !status.Terminal() {
		t.Fatalf("closed job was reactivated as %s", status)
	}
}

func TestReconnectWaitsForOldJobInputWrite(t *testing.T) {
	shell := newReconnectWhileWritingShell()
	s := &Session{ID: "session", shell: shell, cfg: SessionConfig{OutputRetentionBytes: 1024}}
	job := newJob(s, []string{"read"}, ModeForeground)
	job.activate()
	s.current = job

	writeDone := make(chan error, 1)
	go func() { writeDone <- s.writeInput(job, []byte("answer\n")) }()
	<-shell.writeStarted
	reconnectDone := make(chan bool, 1)
	go func() { reconnectDone <- s.tryReconnect(shell) }()

	select {
	case <-shell.reconnectStarted:
		t.Fatal("transport reconnected while old-job input write was still active")
	case <-time.After(300 * time.Millisecond):
	}
	close(shell.releaseWrite)
	if err := <-writeDone; err != nil {
		t.Fatalf("input write: %v", err)
	}
	select {
	case <-shell.reconnectStarted:
	case <-time.After(time.Second):
		t.Fatal("reconnect did not proceed after input write completed")
	}
	s.close()
	close(shell.releaseReconnect)
	select {
	case <-reconnectDone:
	case <-time.After(time.Second):
		t.Fatal("reconnect loop did not stop after session close")
	}
}

func TestEarlyReconnectResetCallbackIsNotLost(t *testing.T) {
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	events := bus.New(16)
	m.SetBus(events)
	feed, cancel := events.Subscribe(16)
	defer cancel()
	shell := newEarlyReconnectShell()
	m.SetRemoteDialer(func(string, int, int) (ShellConn, error) { return shell, nil })

	sess, err := m.CreateSession("agent", "remote", "shell")
	if err != nil {
		t.Fatalf("create session through early reconnect: %v", err)
	}
	seenCreated := false
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-feed:
			switch event.Type {
			case bus.EvSessionCreated:
				seenCreated = true
			case bus.EvSessionReset:
				if !seenCreated {
					t.Fatal("session.reset was delivered before session.created")
				}
				if event.SessionID != sess.ID {
					t.Fatalf("reset session = %q, want %q", event.SessionID, sess.ID)
				}
				shell.mu.Lock()
				reconnects := shell.reconnects
				shell.mu.Unlock()
				if reconnects != 1 {
					t.Fatalf("reconnect count = %d, want 1", reconnects)
				}
				return
			}
		case <-deadline:
			t.Fatal("early reconnect lost its session.reset callback")
		}
	}
}
