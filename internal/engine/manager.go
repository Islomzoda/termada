package engine

import (
	"sync"
	"time"

	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/output"
)

// Config configures the engine (a subset of spec §24 defaults).
type Config struct {
	OutputRetentionBytes int
	MaxForegroundJobs    int
	DefaultTimeoutMS     int
	RedactionPatterns    []string
}

// DefaultConfig returns sane defaults.
func DefaultConfig() Config {
	return Config{
		OutputRetentionBytes: 5_000_000,
		MaxForegroundJobs:    8,
		DefaultTimeoutMS:     30_000,
	}
}

// Manager owns all sessions and the global job registry (spec §9/§10).
type Manager struct {
	cfg      Config
	redactor *output.Redactor

	mu       sync.Mutex
	sessions map[string]*Session
	jobs     map[string]*Job
	defaults map[string]string // owner -> default session id
}

// NewManager builds a manager.
func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg:      cfg,
		redactor: output.NewRedactor(cfg.RedactionPatterns),
		sessions: map[string]*Session{},
		jobs:     map[string]*Job{},
		defaults: map[string]string{},
	}
}

// SessionInfo is the JSON-facing snapshot of a session.
type SessionInfo struct {
	SessionID  string `json:"session_id"`
	Target     string `json:"target"`
	Mode       string `json:"mode"`
	Owner      string `json:"owner"`
	ActiveJobs int    `json:"active_jobs"`
}

// CreateSession creates a persistent-shell session for the given owner.
func (m *Manager) CreateSession(owner, target, mode string) (*Session, error) {
	if target == "" {
		target = "local"
	}
	if target != "local" {
		return nil, errs.New(errs.NotSupported, "remote sessions are a phase-2 feature")
	}
	if mode == "" {
		mode = "shell"
	}
	sess, err := newSession(owner, target, mode, SessionConfig{OutputRetentionBytes: m.cfg.OutputRetentionBytes}, m.redactor)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()
	return sess, nil
}

// resolveSession returns the named session, or the owner's default session
// (creating one if needed) when id is empty (spec SS-4).
func (m *Manager) resolveSession(owner, id string) (*Session, error) {
	if id != "" {
		m.mu.Lock()
		sess := m.sessions[id]
		m.mu.Unlock()
		if sess == nil {
			return nil, errs.New(errs.NotFound, "session %s not found", id)
		}
		return sess, nil
	}
	m.mu.Lock()
	defID := m.defaults[owner]
	sess := m.sessions[defID]
	m.mu.Unlock()
	if sess != nil {
		return sess, nil
	}
	sess, err := m.CreateSession(owner, "local", "shell")
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.defaults[owner] = sess.ID
	m.mu.Unlock()
	return sess, nil
}

func (m *Manager) activeForeground() int {
	n := 0
	for _, s := range m.sessions {
		if s.currentJob() != nil {
			n++
		}
	}
	return n
}

// Start runs a command asynchronously and returns the job immediately (EX-2).
func (m *Manager) Start(owner, sessionID string, command []string, mode string) (*Job, error) {
	sess, err := m.resolveSession(owner, sessionID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.cfg.MaxForegroundJobs > 0 && m.activeForeground() >= m.cfg.MaxForegroundJobs {
		m.mu.Unlock()
		return nil, errs.New(errs.ParallelismExceeded, "max foreground jobs (%d) reached", m.cfg.MaxForegroundJobs)
	}
	m.mu.Unlock()

	job, err := sess.exec(command, mode)
	if job != nil {
		m.mu.Lock()
		m.jobs[job.ID] = job
		m.mu.Unlock()
	}
	return job, err
}

// RunResult is the result of a blocking exec_run.
type RunResult struct {
	Info
	Stdout     string `json:"stdout"`
	NextCursor string `json:"next_cursor"`
	Truncated  bool   `json:"truncated"`
}

// Run starts a command and waits up to timeout for completion. If it does not
// finish in time it returns with the current (running/backgrounded) status and
// the output so far (spec EX-7).
func (m *Manager) Run(owner, sessionID string, command []string, mode string, timeoutMS int) (*RunResult, error) {
	job, err := m.Start(owner, sessionID, command, mode)
	if err != nil {
		return nil, err
	}
	if timeoutMS <= 0 {
		timeoutMS = m.cfg.DefaultTimeoutMS
	}
	// background mode hands control back almost immediately after a short grace
	// to capture any startup output; auto/foreground wait up to the timeout.
	wait := time.Duration(timeoutMS) * time.Millisecond
	if mode == ModeBackground {
		wait = 250 * time.Millisecond
	}
	select {
	case <-job.Done():
	case <-time.After(wait):
	}
	chunk, next, gap := job.clean.ReadFrom(0)
	info := job.info()
	if !info.Status.Terminal() && mode == ModeBackground {
		info.Status = StatusBackgrounded
	}
	return &RunResult{
		Info:       info,
		Stdout:     string(chunk),
		NextCursor: output.EncodeCursor(next),
		Truncated:  gap,
	}, nil
}

// PollResult is returned by exec_poll.
type PollResult struct {
	Info
	StdoutChunk string `json:"stdout_chunk"`
	NextCursor  string `json:"next_cursor"`
	Gap         bool   `json:"gap,omitempty"`
}

// Poll returns incremental output from the cursor onward plus current status.
func (m *Manager) Poll(jobID, cursor string) (*PollResult, error) {
	job, err := m.getJob(jobID)
	if err != nil {
		return nil, err
	}
	off, err := output.DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	chunk, next, gap := job.clean.ReadFrom(off)
	return &PollResult{
		Info:        job.info(),
		StdoutChunk: string(chunk),
		NextCursor:  output.EncodeCursor(next),
		Gap:         gap,
	}, nil
}

// TailResult is returned by logs_tail.
type TailResult struct {
	Lines      string `json:"lines"`
	NextCursor string `json:"next_cursor"`
	Gap        bool   `json:"gap,omitempty"`
}

// Tail returns output from the cursor (or, if empty, the whole retained stream).
func (m *Manager) Tail(jobID, cursor string) (*TailResult, error) {
	job, err := m.getJob(jobID)
	if err != nil {
		return nil, err
	}
	off, err := output.DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	chunk, next, gap := job.clean.ReadFrom(off)
	return &TailResult{
		Lines:      string(chunk),
		NextCursor: output.EncodeCursor(next),
		Gap:        gap,
	}, nil
}

// Write sends input to a job's PTY. If secret is true the value is registered
// for redaction and is never echoed/logged (spec IN-3/§3a).
func (m *Manager) Write(jobID, input string, appendNewline, secret bool) error {
	job, err := m.getJob(jobID)
	if err != nil {
		return err
	}
	if secret {
		m.redactor.AddLiteral(input)
	}
	data := []byte(input)
	if appendNewline {
		data = append(data, '\n')
	}
	return job.sess.writeInput(data)
}

// Signal sends a signal to the running command's process group (spec EX-5/§18b).
func (m *Manager) Signal(jobID, signal string) error {
	job, err := m.getJob(jobID)
	if err != nil {
		return err
	}
	sig, serr := mapSignal(signal)
	if serr != nil {
		return serr
	}
	sess := job.sess
	if sess.currentJob() != job {
		return errs.New(errs.NotFound, "job %s is not currently running", jobID)
	}
	pgid, gerr := foregroundPgid(sess.shell.f.Fd())
	if gerr != nil {
		return errs.New(errs.Internal, "get foreground pgid: %v", gerr)
	}
	if pgid == sess.shell.pid() {
		return errs.New(errs.NotFound, "no command is running in session %s", sess.ID)
	}
	job.requestKill(signal)
	if kerr := killGroup(pgid, sig); kerr != nil {
		return errs.New(errs.Internal, "signal: %v", kerr)
	}
	return nil
}

// Kill force-terminates a job (SIGKILL to its process group).
func (m *Manager) Kill(jobID string) error {
	return m.Signal(jobID, "SIGKILL")
}

// ListJobs returns job snapshots filtered by active|recent|all.
func (m *Manager) ListJobs(filter string) []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Info{}
	for _, j := range m.jobs {
		info := j.info()
		switch filter {
		case "active":
			if info.Status.Terminal() {
				continue
			}
		case "recent":
			if !info.Status.Terminal() {
				continue
			}
		}
		out = append(out, info)
	}
	return out
}

// ListSessions returns session snapshots.
func (m *Manager) ListSessions() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []SessionInfo{}
	for _, s := range m.sessions {
		active := 0
		if s.currentJob() != nil {
			active = 1
		}
		out = append(out, SessionInfo{
			SessionID:  s.ID,
			Target:     s.Target,
			Mode:       s.Mode,
			Owner:      s.Owner,
			ActiveJobs: active,
		})
	}
	return out
}

// CloseSession closes and removes a session.
func (m *Manager) CloseSession(id string) error {
	m.mu.Lock()
	sess := m.sessions[id]
	if sess == nil {
		m.mu.Unlock()
		return errs.New(errs.NotFound, "session %s not found", id)
	}
	delete(m.sessions, id)
	for owner, def := range m.defaults {
		if def == id {
			delete(m.defaults, owner)
		}
	}
	m.mu.Unlock()
	sess.close()
	return nil
}

// Shutdown closes every session (graceful stop).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = map[string]*Session{}
	m.defaults = map[string]string{}
	m.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}

func (m *Manager) getJob(id string) (*Job, error) {
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job == nil {
		return nil, errs.New(errs.NotFound, "job %s not found", id)
	}
	return job, nil
}
