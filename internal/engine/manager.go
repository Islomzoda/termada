package engine

import (
	"strings"
	"sync"
	"time"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/ids"
	"github.com/termada/termada/internal/output"
	"github.com/termada/termada/internal/policy"
)

// Config configures the engine (a subset of spec §24 defaults).
type Config struct {
	OutputRetentionBytes int
	MaxForegroundJobs    int
	DefaultTimeoutMS     int
	ConfirmTimeoutMS     int
	RedactionPatterns    []string
}

// DefaultConfig returns sane defaults.
func DefaultConfig() Config {
	return Config{
		OutputRetentionBytes: 5_000_000,
		MaxForegroundJobs:    8,
		DefaultTimeoutMS:     30_000,
		ConfirmTimeoutMS:     120_000,
	}
}

// Manager owns all sessions and the global job registry (spec §9/§10).
type Manager struct {
	cfg      Config
	redactor *output.Redactor

	bus           *bus.Bus
	pol           *policy.Engine
	agentPolicies map[string]string

	persistPath string
	snapshotDir string
	recovered   []Info // jobs recovered from a previous run (orphaned/terminal)

	mu       sync.Mutex
	sessions map[string]*Session
	jobs     map[string]*Job
	defaults map[string]string          // owner -> default session id
	pending  map[string]*pendingConfirm // confirmation_id -> pending exec
	recipes  map[string]Recipe
}

// NewManager builds a manager.
func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg:           cfg,
		redactor:      output.NewRedactor(cfg.RedactionPatterns),
		agentPolicies: map[string]string{},
		sessions:      map[string]*Session{},
		jobs:          map[string]*Job{},
		defaults:      map[string]string{},
		pending:       map[string]*pendingConfirm{},
		recipes:       map[string]Recipe{},
	}
}

// Bus returns the event bus, or nil if none was set.
func (m *Manager) Bus() *bus.Bus { return m.bus }

// Redactor exposes the shared redactor (used by audit and other surfaces).
func (m *Manager) Redactor() *output.Redactor { return m.redactor }

// SetBus attaches an event bus for observability.
func (m *Manager) SetBus(b *bus.Bus) { m.bus = b }

// SetPolicy attaches the policy engine and the per-agent policy mapping.
func (m *Manager) SetPolicy(p *policy.Engine, agentPolicies map[string]string) {
	m.pol = p
	if agentPolicies != nil {
		m.agentPolicies = agentPolicies
	}
}

// Policy returns the policy engine (may be nil).
func (m *Manager) Policy() *policy.Engine { return m.pol }

// AgentPolicy returns the policy name for an agent.
func (m *Manager) AgentPolicy(agent string) string { return m.agentPolicies[agent] }

func (m *Manager) publish(e bus.Event) {
	if m.bus != nil {
		m.bus.Publish(e)
	}
}

// pendingConfirm is a command awaiting human approval (spec §18a).
type pendingConfirm struct {
	ID       string
	Job      *Job
	Owner    string
	Sess     *Session
	Argv     []string
	Mode     string
	Reason   string
	Matched  string
	Created  time.Time
	resolved bool
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
	m.publish(bus.Event{Type: bus.EvSessionCreated, AgentID: owner, SessionID: sess.ID,
		Data: map[string]any{"target": target, "mode": mode}})
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
// The command is first classified by policy: denied commands error, commands
// requiring confirmation are parked in awaiting_confirmation (spec §18/§18a).
func (m *Manager) Start(owner, sessionID string, command []string, mode string) (*Job, error) {
	sess, err := m.resolveSession(owner, sessionID)
	if err != nil {
		return nil, err
	}

	if m.pol != nil {
		dec := m.pol.Evaluate(m.agentPolicies[owner], command)
		switch dec.Decision {
		case policy.Deny:
			m.publish(bus.Event{Type: bus.EvPolicyDenied, AgentID: owner, SessionID: sess.ID,
				Message: strings.Join(command, " "),
				Data:    map[string]any{"reason": dec.Reason, "matched": dec.Matched}})
			return nil, errs.New(errs.DeniedByPolicy, "command denied by policy (%s)", dec.Reason)
		case policy.Confirm:
			return m.enqueueConfirm(owner, sess, command, mode, dec), nil
		}
	}

	m.mu.Lock()
	if m.cfg.MaxForegroundJobs > 0 && m.activeForeground() >= m.cfg.MaxForegroundJobs {
		m.mu.Unlock()
		return nil, errs.New(errs.ParallelismExceeded, "max foreground jobs (%d) reached", m.cfg.MaxForegroundJobs)
	}
	m.mu.Unlock()

	job, err := sess.exec(command, mode)
	if job != nil {
		m.register(job)
		if err == nil {
			m.publishStarted(job)
			m.watch(job)
		}
	}
	return job, err
}

func (m *Manager) register(job *Job) {
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()
	m.persist()
}

func (m *Manager) publishStarted(job *Job) {
	m.publish(bus.Event{Type: bus.EvJobStarted, AgentID: job.sess.Owner, SessionID: job.SessionID,
		JobID: job.ID, Message: strings.Join(job.Command, " ")})
}

// watch publishes a job.finished event when the job reaches a terminal state.
func (m *Manager) watch(job *Job) {
	go func() {
		<-job.Done()
		info := job.info()
		m.publish(bus.Event{Type: bus.EvJobFinished, AgentID: job.sess.Owner, SessionID: job.SessionID,
			JobID: job.ID, Message: string(info.Status),
			Data: map[string]any{"status": info.Status, "exit_code": info.ExitCode, "reason": info.Reason}})
		m.persist()
	}()
}

// enqueueConfirm parks a command awaiting human approval and returns the job in
// awaiting_confirmation. A timer denies it by default after the configured
// timeout (spec §18a: deny-by-default).
func (m *Manager) enqueueConfirm(owner string, sess *Session, command []string, mode string, dec policy.Result) *Job {
	job := newConfirmJob(sess, command, mode)
	cid := ids.New("cnf")
	job.setConfirmID(cid)
	m.register(job)
	pc := &pendingConfirm{ID: cid, Job: job, Owner: owner, Sess: sess, Argv: command, Mode: mode,
		Reason: dec.Reason, Matched: dec.Matched, Created: time.Now()}
	m.mu.Lock()
	m.pending[cid] = pc
	m.mu.Unlock()
	m.publish(bus.Event{Type: bus.EvConfirmRequested, AgentID: owner, SessionID: sess.ID, JobID: job.ID,
		Message: strings.Join(command, " "),
		Data:    map[string]any{"confirmation_id": cid, "matched": dec.Matched}})

	timeout := time.Duration(m.cfg.ConfirmTimeoutMS) * time.Millisecond
	if timeout > 0 {
		go func() {
			time.Sleep(timeout)
			_ = m.resolveConfirm(cid, false, "timed out", "system")
		}()
	}
	return job
}

func (m *Manager) resolveConfirm(cid string, approved bool, reason, by string) error {
	m.mu.Lock()
	pc := m.pending[cid]
	if pc == nil || pc.resolved {
		m.mu.Unlock()
		return errs.New(errs.NotFound, "confirmation %s not found or already resolved", cid)
	}
	pc.resolved = true
	delete(m.pending, cid)
	m.mu.Unlock()

	if approved {
		if err := pc.Sess.startJob(pc.Job, quoteArgv(pc.Argv)); err != nil {
			pc.Job.finalize(-1, StatusFailed, "exec after approve: "+err.Error())
			m.publish(bus.Event{Type: bus.EvConfirmResolved, JobID: pc.Job.ID,
				Data: map[string]any{"confirmation_id": cid, "approved": false, "by": by, "reason": err.Error()}})
			return err
		}
		m.publishStarted(pc.Job)
		m.watch(pc.Job)
		m.publish(bus.Event{Type: bus.EvConfirmResolved, JobID: pc.Job.ID, AgentID: pc.Owner,
			Data: map[string]any{"confirmation_id": cid, "approved": true, "by": by}})
		return nil
	}
	pc.Job.finalize(-1, StatusFailed, "confirmation "+reason+" (by "+by+")")
	m.publish(bus.Event{Type: bus.EvConfirmResolved, JobID: pc.Job.ID, AgentID: pc.Owner,
		Data: map[string]any{"confirmation_id": cid, "approved": false, "by": by, "reason": reason}})
	return nil
}

// Approve releases a confirmation-gated command for execution.
func (m *Manager) Approve(confirmID, by string) error {
	return m.resolveConfirm(confirmID, true, "approved", by)
}

// Deny rejects a confirmation-gated command.
func (m *Manager) Deny(confirmID, by string) error {
	return m.resolveConfirm(confirmID, false, "denied", by)
}

// PendingInfo describes a command awaiting confirmation.
type PendingInfo struct {
	ConfirmationID string   `json:"confirmation_id"`
	JobID          string   `json:"job_id"`
	AgentID        string   `json:"agent_id"`
	SessionID      string   `json:"session_id"`
	Command        []string `json:"command"`
	Matched        string   `json:"matched"`
	WaitingMS      int64    `json:"waiting_ms"`
}

// ListPending returns all commands awaiting confirmation.
func (m *Manager) ListPending() []PendingInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []PendingInfo{}
	for _, pc := range m.pending {
		out = append(out, PendingInfo{
			ConfirmationID: pc.ID,
			JobID:          pc.Job.ID,
			AgentID:        pc.Owner,
			SessionID:      pc.Sess.ID,
			Command:        pc.Argv,
			Matched:        pc.Matched,
			WaitingMS:      time.Since(pc.Created).Milliseconds(),
		})
	}
	return out
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
	OutputHeld  bool   `json:"output_held,omitempty"`
}

// Poll returns incremental output for the agent (respects an output hold).
func (m *Manager) Poll(jobID, cursor string) (*PollResult, error) {
	return m.PollFor(jobID, cursor, false)
}

// PollFor returns incremental output from the cursor onward plus current status.
// When human is false and the job's output is held, no new bytes are returned
// (the agent is paused); the human dashboard passes human=true to always stream.
func (m *Manager) PollFor(jobID, cursor string, human bool) (*PollResult, error) {
	job, err := m.getJob(jobID)
	if err != nil {
		return nil, err
	}
	off, err := output.DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	if _, ho := job.holds(); ho && !human {
		return &PollResult{Info: job.info(), StdoutChunk: "", NextCursor: cursor, OutputHeld: true}, nil
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
// for redaction and is never echoed/logged (spec IN-3/§3a). human=true marks
// input typed by a person at the dashboard/TUI; when a job's input is held
// (human takeover), agent writes (human=false) are rejected.
func (m *Manager) Write(jobID, input string, appendNewline, secret, human bool) error {
	job, err := m.getJob(jobID)
	if err != nil {
		return err
	}
	if hi, _ := job.holds(); hi && !human {
		return errs.New(errs.DeniedByPolicy, "input is held by a human operator")
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

// Hold sets the human-intervention flags for a job (nil = unchanged). Used by
// the dashboard/CLI so a person can take over input and/or pause the output the
// agent receives, while still watching the live stream themselves.
func (m *Manager) Hold(jobID string, input, output *bool) error {
	job, err := m.getJob(jobID)
	if err != nil {
		return err
	}
	job.setHold(input, output)
	if m.bus != nil {
		hi, ho := job.holds()
		m.publish(bus.Event{Type: "job.hold", JobID: jobID, SessionID: job.SessionID,
			Message: "human intervention", Data: map[string]any{"hold_input": hi, "hold_output": ho}})
	}
	return nil
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
	keep := func(info Info) {
		switch filter {
		case "active":
			if info.Status.Terminal() {
				return
			}
		case "recent":
			if !info.Status.Terminal() {
				return
			}
		}
		out = append(out, info)
	}
	for _, j := range m.jobs {
		keep(j.info())
	}
	for _, in := range m.recovered {
		keep(in)
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

func (m *Manager) getSession(id string) (*Session, error) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return nil, errs.New(errs.NotFound, "session %s not found", id)
	}
	return s, nil
}

// SessionStreamResult is incremental session-terminal output.
type SessionStreamResult struct {
	Chunk      string `json:"chunk"`
	NextCursor string `json:"next_cursor"`
	Gap        bool   `json:"gap,omitempty"`
	Closed     bool   `json:"closed"`
}

// SessionTail returns the session's continuous terminal output from the cursor
// onward (the whole shell, across all jobs) — what the dashboard streams as one
// live terminal for the session.
func (m *Manager) SessionTail(sessionID, cursor string) (*SessionStreamResult, error) {
	s, err := m.getSession(sessionID)
	if err != nil {
		return nil, err
	}
	off, err := output.DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	chunk, next, gap := s.clean.ReadFrom(off)
	return &SessionStreamResult{
		Chunk:      string(chunk),
		NextCursor: output.EncodeCursor(next),
		Gap:        gap,
		Closed:     s.isClosed(),
	}, nil
}

// SessionWriteInput sends operator input directly to a session's shell PTY.
// If the current foreground job's input is held, agent input (human=false) is
// rejected; operator input (human=true) always goes through.
func (m *Manager) SessionWriteInput(sessionID, input string, appendNewline, human bool) error {
	s, err := m.getSession(sessionID)
	if err != nil {
		return err
	}
	if cur := s.currentJob(); cur != nil {
		if hi, _ := cur.holds(); hi && !human {
			return errs.New(errs.DeniedByPolicy, "input is held by a human operator")
		}
	}
	data := []byte(input)
	if appendNewline {
		data = append(data, '\n')
	}
	return s.writeInput(data)
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
