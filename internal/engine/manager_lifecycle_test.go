package engine

import (
	"bytes"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/errs"
)

// lifecycleShell implements just enough of the marker protocol to initialise a
// session without a real process. Writes can be gated after init, which makes
// shutdown races deterministic instead of timing-dependent.
type lifecycleShell struct {
	responses    chan []byte
	closed       chan struct{}
	eof          chan struct{}
	closeOnce    sync.Once
	eofOnce      sync.Once
	writeMu      sync.Mutex
	writes       int
	blockAfter   int
	writeStarted chan struct{}
	closeCount   atomic.Int32
}

func newLifecycleShell(blockAfter int) *lifecycleShell {
	return &lifecycleShell{
		responses:    make(chan []byte, 4),
		closed:       make(chan struct{}),
		eof:          make(chan struct{}),
		blockAfter:   blockAfter,
		writeStarted: make(chan struct{}),
	}
}

func (s *lifecycleShell) Read(p []byte) (int, error) {
	select {
	case response := <-s.responses:
		return copy(p, response), nil
	case <-s.eof:
		return 0, io.EOF
	case <-s.closed:
		return 0, io.EOF
	}
}

func (s *lifecycleShell) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	s.writes++
	writeNumber := s.writes
	s.writeMu.Unlock()
	if s.blockAfter > 0 && writeNumber > s.blockAfter {
		select {
		case <-s.writeStarted:
		default:
			close(s.writeStarted)
		}
		<-s.closed
		return 0, io.ErrClosedPipe
	}
	start := bytes.Index(p, []byte("TERMADA:"))
	if start < 0 {
		return len(p), nil
	}
	start += len("TERMADA:")
	endOffset := bytes.Index(p[start:], []byte(":%d"))
	if endOffset < 0 {
		return len(p), nil
	}
	marker := string(p[start : start+endOffset])
	response := []byte("\x1eTERMADA:" + marker + ":0\x1e")
	select {
	case s.responses <- response:
	case <-s.closed:
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func (s *lifecycleShell) Close() error {
	s.closeOnce.Do(func() {
		s.closeCount.Add(1)
		close(s.closed)
	})
	return nil
}

func (s *lifecycleShell) Signal(string) error { return nil }

func (s *lifecycleShell) triggerEOF() { s.eofOnce.Do(func() { close(s.eof) }) }

func TestCreateSessionClosesLateResourceAfterShutdown(t *testing.T) {
	m := NewManager(DefaultConfig())
	shell := newLifecycleShell(0)
	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	m.SetRemoteDialer(func(string, int, int) (ShellConn, error) {
		close(dialStarted)
		<-releaseDial
		return shell, nil
	})

	type result struct {
		session *Session
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		session, err := m.CreateSession("agent", "remote", "shell")
		resultCh <- result{session: session, err: err}
	}()
	<-dialStarted // the session reservation is now held, but no shell is registered
	m.Shutdown()
	close(releaseDial)

	select {
	case got := <-resultCh:
		if got.session != nil || !isManagerClosedError(got.err) {
			t.Fatalf("late CreateSession = (%v, %v), want nil manager-closed error", got.session, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late CreateSession did not finish")
	}
	if got := shell.closeCount.Load(); got != 1 {
		t.Fatalf("late shell close count = %d, want 1", got)
	}
	if got := len(m.ListSessions()); got != 0 {
		t.Fatalf("registered %d sessions after shutdown", got)
	}
	m.mu.Lock()
	reserved := m.reservedSessions
	m.mu.Unlock()
	if reserved != 0 {
		t.Fatalf("session reservations after late cleanup = %d, want 0", reserved)
	}
}

func TestShutdownRejectsNewResourcesAndInflightJob(t *testing.T) {
	m := NewManager(DefaultConfig())
	shell := newLifecycleShell(1) // init succeeds; the first command write blocks
	m.SetRemoteDialer(func(string, int, int) (ShellConn, error) { return shell, nil })
	session, err := m.CreateSession("agent", "remote", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	startResult := make(chan error, 1)
	go func() {
		_, startErr := m.Start("agent", session.ID, []string{"true"}, ModeForeground)
		startResult <- startErr
	}()
	<-shell.writeStarted
	m.Shutdown()
	if err := <-startResult; !isManagerClosedError(err) {
		t.Fatalf("in-flight Start error = %v, want manager-closed", err)
	}
	if !m.Closed() {
		t.Fatal("manager did not retain its closed state")
	}
	if _, err := m.CreateSession("agent", "local", "shell"); !isManagerClosedError(err) {
		t.Fatalf("CreateSession after shutdown error = %v, want manager-closed", err)
	}
	if err := m.reserveSession("agent"); !isManagerClosedError(err) {
		t.Fatalf("reservation after shutdown error = %v, want manager-closed", err)
	}
	if _, err := m.Start("agent", session.ID, []string{"true"}, ModeForeground); !isManagerClosedError(err) {
		t.Fatalf("Start after shutdown error = %v, want manager-closed", err)
	}
	m.mu.Lock()
	jobs, reservations := len(m.jobs), m.reservedJobs
	m.mu.Unlock()
	if jobs != 0 || reservations != 0 {
		t.Fatalf("post-shutdown jobs=%d reservations=%d, want zero", jobs, reservations)
	}
}

func TestResolveSessionDoesNotRestoreDefaultAfterShutdown(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	m := NewManager(DefaultConfig())
	b := bus.New(8)
	m.SetBus(b)
	created := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	cancel := b.SubscribeReliable(func(event bus.Event) error {
		if event.Type == bus.EvSessionCreated {
			once.Do(func() { close(created) })
			<-release
		}
		return nil
	})
	defer cancel()

	result := make(chan error, 1)
	go func() {
		_, err := m.Start("agent", "", []string{"true"}, ModeForeground)
		result <- err
	}()
	<-created // CreateSession registered the shell but has not returned to resolveSession
	m.Shutdown()
	close(release)
	if err := <-result; !isManagerClosedError(err) {
		t.Fatalf("default Start error = %v, want manager-closed", err)
	}
	m.mu.Lock()
	defaults, sessions := len(m.defaults), len(m.sessions)
	m.mu.Unlock()
	if defaults != 0 || sessions != 0 {
		t.Fatalf("post-shutdown defaults=%d sessions=%d, want zero", defaults, sessions)
	}
}

func TestEOFClosesTransportAndDefaultSessionRecovers(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required for replacement default session")
	}
	m := NewManager(DefaultConfig())
	t.Cleanup(m.Shutdown)
	shell := newLifecycleShell(0)
	m.SetRemoteDialer(func(string, int, int) (ShellConn, error) { return shell, nil })

	old, err := m.CreateSession("agent", "remote", "shell")
	if err != nil {
		t.Fatalf("create old session: %v", err)
	}
	m.mu.Lock()
	m.defaults["agent"] = old.ID
	m.mu.Unlock()

	shell.triggerEOF()
	deadline := time.Now().Add(2 * time.Second)
	for (!old.isClosed() || shell.closeCount.Load() != 1) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !old.isClosed() {
		t.Fatal("session did not become closed after EOF")
	}
	if got := shell.closeCount.Load(); got != 1 {
		t.Fatalf("transport close count after EOF = %d, want 1", got)
	}

	replacement, err := m.resolveSession("agent", "")
	if err != nil {
		t.Fatalf("resolve replacement default: %v", err)
	}
	if replacement.ID == old.ID || replacement.isClosed() {
		t.Fatalf("replacement default = %s closed=%v, old=%s", replacement.ID, replacement.isClosed(), old.ID)
	}
	m.mu.Lock()
	_, oldRegistered := m.sessions[old.ID]
	defaultID := m.defaults["agent"]
	m.mu.Unlock()
	if oldRegistered || defaultID != replacement.ID {
		t.Fatalf("old_registered=%v default=%q, want replacement %q", oldRegistered, defaultID, replacement.ID)
	}

	old.close()
	if got := shell.closeCount.Load(); got != 1 {
		t.Fatalf("idempotent close count = %d, want 1", got)
	}
}

func isManagerClosedError(err error) bool {
	structured, ok := err.(*errs.Error)
	return ok && structured.Code == errs.NotFound && structured.Message == "engine manager is shut down"
}
