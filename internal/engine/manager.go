package engine

import (
	"sort"
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
	MaxOutputBytes       int // per-call cap on stdout returned by exec_run/exec_poll/logs_tail (0 = use OutputRetentionBytes)
	PTYCols              int // initial PTY width for new sessions (0 = default 200)
	MaxForegroundJobs    int
	MaxBackgroundJobs    int // separate cap for background/auto-backgrounded jobs (0 = unlimited); these no longer count against MaxForegroundJobs
	MaxJobsPerAgent      int // per-agent concurrent-job quota (0 = unlimited, MA-3)
	MaxJobRuntimeMS      int // 0 = no cap; reaper SIGKILLs jobs running longer (runaway/hung safety net)
	DefaultTimeoutMS     int
	ConfirmTimeoutMS     int
	RedactionPatterns    []string
}

const (
	maxCommandArgs      = 4096
	maxCommandBytes     = 256 << 10
	maxAgentIDBytes     = 128
	maxAgentTokenBytes  = 4096
	maxSessionsPerOwner = 32
	maxSessionsTotal    = 128
	maxPendingPerOwner  = 32
	maxPendingTotal     = 128
	maxJobsInMemory     = 256
	maxExecWaitMS       = 24 * 60 * 60 * 1000
)

// DefaultConfig returns sane defaults.
func DefaultConfig() Config {
	return Config{
		OutputRetentionBytes: 5_000_000,
		MaxOutputBytes:       100_000,
		PTYCols:              200,
		MaxForegroundJobs:    8,
		DefaultTimeoutMS:     30_000,
		ConfirmTimeoutMS:     120_000,
	}
}

// maxOutputBytes returns the effective per-call output cap.
func (m *Manager) maxOutputBytes() int {
	n := m.cfg.MaxOutputBytes
	if n <= 0 {
		n = m.cfg.OutputRetentionBytes
	}
	if n <= 0 {
		n = 100_000
	}
	if n > 1<<20 {
		n = 1 << 20
	}
	return n
}

// Manager owns all sessions and the global job registry (spec §9/§10).
type Manager struct {
	cfg      Config
	redactor *output.Redactor

	bus              *bus.Bus
	pol              *policy.Engine
	agentPolicies    map[string]string
	agentTokens      map[string]string   // token -> agent id (non-spoofable identity)
	tokenBoundAgents map[string]struct{} // ids that may only be claimed with their configured token
	timeoutClasses   map[string]int      // class name -> timeout ms (LR-2)
	auditOK          func() bool         // audit health probe; dangerous ops fail closed if false
	remoteDial       RemoteDialer        // opens a shell to a named remote server (wired by daemon)
	remoteFiles      RemoteFileOps       // file_read/file_write against a remote target (wired by daemon)
	forwards         ForwardOps          // local→remote SSH port forwards (wired by daemon)

	persistPath    string // guarded by persistMu
	persistErr     error  // last registry write/recovery error; guarded by persistMu
	snapshotDir    string
	protectedPaths []string    // canonical paths file_read/file_write refuse (C2/FS-3)
	spawn          SpawnConfig // how local agent shells are launched (uid separation, SEC-8)
	recovered      []Info      // jobs recovered from a previous run (orphaned/terminal)

	mu                 sync.Mutex
	persistMu          sync.Mutex
	watchWG            sync.WaitGroup
	defaultMu          [32]sync.Mutex
	sessions           map[string]*Session
	jobs               map[string]*Job
	defaults           map[string]string          // owner -> default session id
	pending            map[string]*pendingConfirm // confirmation_id -> pending exec
	recipes            map[string]Recipe
	agents             map[string]*AgentStat // agent id -> activity stats
	reservedForeground int                   // admitted foreground starts not yet represented by the live-job scan
	reservedBackground int                   // admitted background starts not yet represented by the live-job scan
	reservedQuotaJobs  map[string]struct{}   // registered confirmation jobs represented by a start reservation
	reservedSessions   int
	reservedByOwner    map[string]int
	reservedJobs       int
	reservedByAgent    map[string]int
	recipeStepTimeout  time.Duration
	closed             bool
}

// NewManager builds a manager.
func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg:               cfg,
		redactor:          output.NewRedactor(cfg.RedactionPatterns),
		agentPolicies:     map[string]string{},
		agentTokens:       map[string]string{},
		tokenBoundAgents:  map[string]struct{}{},
		sessions:          map[string]*Session{},
		jobs:              map[string]*Job{},
		defaults:          map[string]string{},
		pending:           map[string]*pendingConfirm{},
		recipes:           map[string]Recipe{},
		agents:            map[string]*AgentStat{},
		reservedByOwner:   map[string]int{},
		reservedByAgent:   map[string]int{},
		reservedQuotaJobs: map[string]struct{}{},
		recipeStepTimeout: 30 * time.Minute,
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

// SetAgentTokens installs token→agent-id bindings for non-spoofable identity.
func (m *Manager) SetAgentTokens(tokens map[string]string) {
	if tokens == nil {
		return
	}
	m.mu.Lock()
	m.agentTokens = make(map[string]string, len(tokens))
	m.tokenBoundAgents = make(map[string]struct{}, len(tokens))
	for token, id := range tokens {
		if !validAgentToken(token) || !validAgentID(id) {
			continue
		}
		m.agentTokens[token] = id
		m.tokenBoundAgents[id] = struct{}{}
	}
	m.mu.Unlock()
}

// AuthenticateAgent binds a request to an agent identity. Presented tokens are
// authoritative and fail closed: an unknown token is never allowed to fall back
// to request JSON. Tokenless self-assertion remains available only for
// unconfigured local/dev identities and never for the empty (operator) owner.
func (m *Manager) AuthenticateAgent(token, asserted string) (string, error) {
	token = strings.TrimSpace(token)
	trimmedAsserted := strings.TrimSpace(asserted)
	if asserted != trimmedAsserted {
		return "", errs.New(errs.InvalidArgument, "agent identity must not have leading or trailing whitespace")
	}
	asserted = trimmedAsserted
	m.mu.Lock()
	defer m.mu.Unlock()
	if token != "" {
		if !validAgentToken(token) {
			return "", errs.New(errs.DeniedByPolicy, "invalid agent token")
		}
		if id, ok := m.agentTokens[token]; ok {
			return id, nil
		}
		return "", errs.New(errs.DeniedByPolicy, "unknown agent token")
	}
	if asserted == "" {
		return "", errs.New(errs.DeniedByPolicy, "agent identity is required")
	}
	if !validAgentID(asserted) {
		return "", errs.New(errs.InvalidArgument, "agent identity must be at most %d bytes and contain no whitespace/control characters", maxAgentIDBytes)
	}
	if _, protected := m.tokenBoundAgents[asserted]; protected {
		return "", errs.New(errs.DeniedByPolicy, "agent %q requires its configured token", asserted)
	}
	return asserted, nil
}

func validAgentID(id string) bool {
	return id != "" && len(id) <= maxAgentIDBytes && strings.IndexFunc(id, func(r rune) bool { return r < 0x21 || r == 0x7f }) < 0
}

func validAgentToken(token string) bool {
	return token != "" && len(token) <= maxAgentTokenBytes && strings.IndexFunc(token, func(r rune) bool { return r < 0x21 || r > 0x7e }) < 0
}

// ResolveAgent is kept for callers that only need the resolved id. Invalid
// credentials resolve to the empty string rather than a self-asserted fallback.
func (m *Manager) ResolveAgent(token, asserted string) string {
	id, _ := m.AuthenticateAgent(token, asserted)
	return id
}

// SetTimeoutClasses installs the per-class adaptive timeouts (LR-2).
func (m *Manager) SetTimeoutClasses(classes map[string]int) {
	m.timeoutClasses = classes
}

// SetSpawnConfig installs how local agent shells are launched (uid separation,
// SEC-8). The daemon resolves and validates it (requires root) before wiring.
func (m *Manager) SetSpawnConfig(sp SpawnConfig) { m.spawn = sp }

// RemoteDialer opens a persistent shell transport to a named remote target (a
// configured server). It is wired by the daemon, which holds the server
// inventory and the vault, so the engine stays free of SSH/vault dependencies.
type RemoteDialer func(target string, cols, rows int) (ShellConn, error)

// SetRemoteDialer installs the remote-session dialer (enables session_create
// against a server name).
func (m *Manager) SetRemoteDialer(d RemoteDialer) { m.remoteDial = d }

// RemoteFileOps performs file_read/file_write against a remote target over SFTP.
// Wired by the daemon (which holds the server inventory + SSH runner); nil in the
// in-process fallback, where remote file ops are refused.
type RemoteFileOps interface {
	ReadFile(target, path string, maxBytes int) (content []byte, size int64, truncated bool, err error)
	WriteFile(target, path, content, mode string) (n int, err error)
}

// SetRemoteFileOps installs the remote file-transfer backend (enables file_read/
// file_write against a remote session).
func (m *Manager) SetRemoteFileOps(ops RemoteFileOps) { m.remoteFiles = ops }

// SetAuditHealth installs a probe for audit-log health. When it reports
// unhealthy, dangerous (confirmation-gated) commands are refused — fail-closed
// (spec RE-7): if we can't record an action, we don't take it.
func (m *Manager) SetAuditHealth(probe func() bool) {
	m.auditOK = probe
}

// classTimeout returns the adaptive timeout (ms) for a command: per-class if
// configured, else the global default. A class value of 0 ("no limit") falls
// back to the default so exec_run doesn't block forever.
func (m *Manager) classTimeout(argv []string) int {
	if v, ok := m.timeoutClasses[classify(argv)]; ok && v > 0 {
		return v
	}
	if v, ok := m.timeoutClasses["default"]; ok && v > 0 {
		return v
	}
	return m.cfg.DefaultTimeoutMS
}

// Policy returns the policy engine (may be nil).
func (m *Manager) Policy() *policy.Engine { return m.pol }

// AgentPolicy returns the policy name for an agent.
func (m *Manager) AgentPolicy(agent string) string { return m.agentPolicies[agent] }

// AgentPolicies returns a copy of the agent→policy-name mapping (read-only view).
func (m *Manager) AgentPolicies() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.agentPolicies))
	for k, v := range m.agentPolicies {
		out[k] = v
	}
	return out
}

func (m *Manager) publish(e bus.Event) error {
	if m.bus != nil {
		return m.bus.Publish(e)
	}
	return nil
}

func managerClosedError() error {
	return errs.New(errs.NotFound, "engine manager is shut down")
}

// Closed reports whether shutdown has begun. Shutdown is a one-way lifecycle
// transition: once this returns true, the manager refuses new live resources.
func (m *Manager) Closed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
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
	timer    *time.Timer
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
	if mode == "" {
		mode = "shell"
	}
	if mode != "shell" {
		return nil, errs.New(errs.InvalidArgument, "unsupported session mode %q", mode)
	}
	if len(target) > 255 || strings.TrimSpace(target) != target || strings.IndexFunc(target, func(r rune) bool { return r < 0x21 || r == 0x7f }) >= 0 {
		return nil, errs.New(errs.InvalidArgument, "session target must be at most 255 bytes and contain no whitespace/control characters")
	}
	if owner != "" && !validAgentID(owner) {
		return nil, errs.New(errs.InvalidArgument, "invalid session owner")
	}
	if err := m.reserveSession(owner); err != nil {
		return nil, err
	}
	reserved := true
	defer func() {
		if reserved {
			m.releaseSessionReservation(owner)
		}
	}()
	scfg := SessionConfig{OutputRetentionBytes: m.cfg.OutputRetentionBytes, PTYCols: m.cfg.PTYCols}
	var sess *Session
	var err error
	if target == "local" {
		sess, err = newSession(owner, target, mode, scfg, m.redactor, m.spawn)
	} else {
		if m.remoteDial == nil {
			return nil, errs.New(errs.NotSupported, "remote sessions require a configured server and unlocked vault")
		}
		conn, derr := m.remoteDial(target, 200, 50)
		if derr != nil {
			return nil, errs.New(errs.ServerUnreachable, "connect to %s: %v", target, derr)
		}
		sess, err = newSessionConn(owner, target, mode, conn, scfg, m.redactor)
	}
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.releaseSessionReservationLocked(owner)
	if m.closed {
		reserved = false
		m.mu.Unlock()
		sess.close()
		return nil, managerClosedError()
	}
	m.sessions[sess.ID] = sess
	reserved = false
	m.mu.Unlock()
	m.publish(bus.Event{Type: bus.EvSessionCreated, AgentID: owner, SessionID: sess.ID,
		Data: map[string]any{"target": target, "mode": mode}})
	sess.setOnReset(func() {
		m.publish(bus.Event{Type: bus.EvSessionReset, AgentID: sess.Owner, SessionID: sess.ID,
			Message: "remote connection dropped; reconnected — cwd/env were reset"})
	})
	m.touchAgent(owner, func(a *AgentStat) { a.Sessions++ })
	return sess, nil
}

func (m *Manager) reserveSession(owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return managerClosedError()
	}
	if len(m.sessions)+m.reservedSessions >= maxSessionsTotal {
		return errs.New(errs.ParallelismExceeded, "daemon reached its session limit (%d)", maxSessionsTotal)
	}
	if owner != "" {
		owned := m.reservedByOwner[owner]
		for _, session := range m.sessions {
			if session.Owner == owner {
				owned++
			}
		}
		if owned >= maxSessionsPerOwner {
			return errs.New(errs.ParallelismExceeded, "agent %q reached its session limit (%d)", owner, maxSessionsPerOwner)
		}
	}
	m.reservedSessions++
	m.reservedByOwner[owner]++
	return nil
}

func (m *Manager) releaseSessionReservation(owner string) {
	m.mu.Lock()
	m.releaseSessionReservationLocked(owner)
	m.mu.Unlock()
}

func (m *Manager) releaseSessionReservationLocked(owner string) {
	m.reservedSessions--
	if m.reservedByOwner[owner] <= 1 {
		delete(m.reservedByOwner, owner)
	} else {
		m.reservedByOwner[owner]--
	}
}

// resolveSession returns the named session, or the owner's default session
// (creating one if needed) when id is empty (spec SS-4).
func (m *Manager) resolveSession(owner, id string) (*Session, error) {
	if id != "" {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, managerClosedError()
		}
		sess := m.sessions[id]
		m.mu.Unlock()
		if sess == nil || (owner != "" && sess.Owner != owner) {
			return nil, errs.New(errs.NotFound, "session %s not found", id)
		}
		return sess, nil
	}
	defaultLock := &m.defaultMu[ownerLockIndex(owner)]
	defaultLock.Lock()
	defer defaultLock.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, managerClosedError()
	}
	defID := m.defaults[owner]
	sess := m.sessions[defID]
	closed := sess != nil && sess.isClosed()
	if closed {
		delete(m.sessions, defID)
		delete(m.defaults, owner)
	}
	m.mu.Unlock()
	if closed {
		// EOF performs this cleanup too; this call is idempotent and covers the
		// narrow window where the logical close became visible first.
		sess.close()
		sess = nil
	}
	if sess != nil {
		return sess, nil
	}
	sess, err := m.CreateSession(owner, "local", "shell")
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		sess.close()
		return nil, managerClosedError()
	}
	if m.sessions[sess.ID] != sess {
		m.mu.Unlock()
		sess.close()
		return nil, errs.New(errs.NotFound, "session %s closed during default-session creation", sess.ID)
	}
	m.defaults[owner] = sess.ID
	m.mu.Unlock()
	return sess, nil
}

func ownerLockIndex(owner string) uint32 {
	var hash uint32 = 2166136261
	for i := 0; i < len(owner); i++ {
		hash ^= uint32(owner[i])
		hash *= 16777619
	}
	return hash % 32
}

// activeForeground counts registered live jobs that hold a foreground slot —
// i.e. everything except jobs marked background, which are gated by
// MaxBackgroundJobs instead so a handful of long-lived dev servers can't block
// every other exec on the daemon (spec-adjacent to LR-1's "dev servers are
// first-class" stance). The caller holds m.mu, so a start reservation is
// atomically replaced by its registered job and can never be counted twice.
func (m *Manager) activeForeground() int {
	n := 0
	for _, j := range m.jobs {
		if _, reserved := m.reservedQuotaJobs[j.ID]; reserved {
			continue
		}
		j.mu.Lock()
		active := j.status == StatusRunning
		background := j.background
		j.mu.Unlock()
		if active && !background {
			n++
		}
	}
	return n
}

// activeBackground counts registered live jobs marked background. See
// activeForeground for why reservations and jobs share m.mu as the transition
// boundary.
func (m *Manager) activeBackground() int {
	n := 0
	for _, j := range m.jobs {
		if _, reserved := m.reservedQuotaJobs[j.ID]; reserved {
			continue
		}
		j.mu.Lock()
		active := j.status == StatusRunning
		background := j.background
		j.mu.Unlock()
		if active && background {
			n++
		}
	}
	return n
}

// markBackgroundWithinQuota atomically reclassifies a live foreground job. If
// the background pool is full, the job keeps occupying its foreground slot.
func (m *Manager) markBackgroundWithinQuota(job *Job) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job.isBackground() {
		return true
	}
	if m.cfg.MaxBackgroundJobs > 0 && m.activeBackground()+m.reservedBackground >= m.cfg.MaxBackgroundJobs {
		return false
	}
	job.markBackground()
	return true
}

// activeForAgent counts an agent's currently-running (non-terminal) jobs, for
// per-agent quotas (MA-3).
func (m *Manager) activeForAgent(owner string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeForAgentLocked(owner)
}

// activeForAgentLocked counts an agent's non-terminal jobs. The caller must hold
// m.mu — this is the form used inside register() so the count-and-insert is one
// atomic critical section (see Start / registerWithQuota).
func (m *Manager) activeForAgentLocked(owner string) int {
	n := 0
	for _, j := range m.jobs {
		j.mu.Lock()
		terminal := j.status.Terminal()
		j.mu.Unlock()
		if !terminal && j.sess.Owner == owner {
			n++
		}
	}
	return n
}

// Start runs a command asynchronously and returns the job immediately (EX-2).
// The command is first classified by policy: denied commands error, commands
// requiring confirmation are parked in awaiting_confirmation (spec §18/§18a).
func (m *Manager) Start(owner, sessionID string, command []string, mode string) (*Job, error) {
	if err := validateCommand(command); err != nil {
		return nil, err
	}
	if mode != "" && mode != ModeAuto && mode != ModeForeground && mode != ModeBackground {
		return nil, errs.New(errs.InvalidArgument, "unknown execution mode %q", mode)
	}
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
			m.touchAgent(owner, func(a *AgentStat) { a.Denied++ })
			return nil, errs.New(errs.DeniedByPolicy, "command denied by policy (%s)", dec.Reason)
		case policy.Confirm:
			if m.auditOK != nil && !m.auditOK() {
				return nil, errs.New(errs.Internal, "audit log unavailable — refusing dangerous command (fail-closed)")
			}
			return m.enqueueConfirm(owner, sess, command, mode, dec)
		}
	}

	// Per-agent quota: this pre-check cheaply rejects the common already-at-quota
	// case before we spawn anything on the shell. It is NOT authoritative — it
	// reads the count and releases m.mu, so two concurrent Starts on DIFFERENT
	// sessions of the same agent can both pass it. The race-free enforcement is in
	// registerWithQuota below, which counts and inserts atomically under m.mu.
	if m.cfg.MaxJobsPerAgent > 0 && m.activeForAgent(owner) >= m.cfg.MaxJobsPerAgent {
		return nil, errs.New(errs.ParallelismExceeded, "agent %q reached its concurrent-job quota (%d)", owner, m.cfg.MaxJobsPerAgent)
	}
	reservedClass := ""
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, managerClosedError()
	}
	if !m.makeJobRoomLocked() || len(m.jobs)+m.reservedJobs >= maxJobsInMemory {
		m.mu.Unlock()
		return nil, errs.New(errs.ParallelismExceeded, "job registry reached its active/history limit (%d)", maxJobsInMemory)
	}
	if m.cfg.MaxJobsPerAgent > 0 && m.activeForAgentLocked(owner)+m.reservedByAgent[owner] >= m.cfg.MaxJobsPerAgent {
		m.mu.Unlock()
		return nil, errs.New(errs.ParallelismExceeded, "agent %q reached its concurrent-job quota (%d)", owner, m.cfg.MaxJobsPerAgent)
	}
	// An explicitly background-mode command is gated by MaxBackgroundJobs, not
	// MaxForegroundJobs: it doesn't occupy a foreground slot once classified (see
	// activeForeground). A command that only LATER gets auto-backgrounded by Run
	// (daemon heuristic / timeout) can't be classified yet at Start time, so it is
	// still checked against MaxForegroundJobs here — same as any other command —
	// and only counts against MaxBackgroundJobs from the moment Run reclassifies
	// it. Reservations make the check-and-start transition race-free.
	if mode == ModeBackground && m.cfg.MaxBackgroundJobs > 0 && m.activeBackground()+m.reservedBackground >= m.cfg.MaxBackgroundJobs {
		m.mu.Unlock()
		return nil, errs.New(errs.ParallelismExceeded, "max background jobs (%d) reached", m.cfg.MaxBackgroundJobs)
	}
	if mode != ModeBackground && m.cfg.MaxForegroundJobs > 0 && m.activeForeground()+m.reservedForeground >= m.cfg.MaxForegroundJobs {
		m.mu.Unlock()
		return nil, errs.New(errs.ParallelismExceeded, "max foreground jobs (%d) reached", m.cfg.MaxForegroundJobs)
	}
	if mode == ModeBackground && m.cfg.MaxBackgroundJobs > 0 {
		m.reservedBackground++
		reservedClass = ModeBackground
	} else if mode != ModeBackground && m.cfg.MaxForegroundJobs > 0 {
		m.reservedForeground++
		reservedClass = ModeForeground
	}
	m.reservedJobs++
	m.reservedByAgent[owner]++
	m.mu.Unlock()

	// Record a durable execution intent before touching the shell. This makes the
	// first audit append/fsync failure fail closed for ordinary commands too; the
	// post-spawn job.started event adds the generated job id.
	if err := m.publish(bus.Event{Type: bus.EvJobStartRequested, AgentID: owner, SessionID: sess.ID,
		Message: strings.Join(command, " "), Data: map[string]any{"mode": mode}}); err != nil {
		m.mu.Lock()
		m.releaseStartReservationLocked(owner, reservedClass)
		m.mu.Unlock()
		return nil, errs.New(errs.Internal, "audit log unavailable - refusing command start: %v", err)
	}

	job, err := sess.exec(command, mode)
	if job != nil && mode == ModeBackground {
		job.markBackground()
	}
	// Transition every reservation -> registered live job atomically. The
	// per-agent reservation is authoritative and was taken before shell I/O, so a
	// concurrent loser never gets to execute first and be killed after the fact.
	m.mu.Lock()
	m.releaseStartReservationLocked(owner, reservedClass)
	closed := m.closed
	var releaseWatch func()
	if !closed && err == nil && job != nil {
		m.jobs[job.ID] = job
		releaseWatch = m.prepareWatchLocked(job)
	}
	m.mu.Unlock()
	if closed {
		sess.close()
		return nil, managerClosedError()
	}
	if job == nil {
		return nil, err
	}
	if err != nil {
		// exec failed before the command ran (e.g. session busy/closed, pty write):
		// register the already-finalized job so its failure stays observable, but
		// skip the quota gate — a failed job neither counts nor needs teardown.
		m.register(job)
		return job, err
	}
	m.persist()
	m.publishStarted(job)
	releaseWatch()
	m.autoAnswerWatch(job, owner)
	m.touchAgent(owner, func(a *AgentStat) { a.Jobs++; a.recordCommand(cmdString(command)) })
	return job, nil
}

func (m *Manager) releaseStartReservationLocked(owner, class string) {
	if class == ModeBackground {
		m.reservedBackground--
	} else if class == ModeForeground {
		m.reservedForeground--
	}
	m.reservedJobs--
	if m.reservedByAgent[owner] <= 1 {
		delete(m.reservedByAgent, owner)
	} else {
		m.reservedByAgent[owner]--
	}
}

func validateCommand(command []string) error {
	if len(command) == 0 || command[0] == "" {
		return errs.New(errs.InvalidArgument, "command must not be empty")
	}
	if len(command) > maxCommandArgs {
		return errs.New(errs.InvalidArgument, "command has %d arguments, exceeds limit %d", len(command), maxCommandArgs)
	}
	total := 0
	for _, arg := range command {
		if strings.IndexByte(arg, 0) >= 0 {
			return errs.New(errs.InvalidArgument, "command arguments must not contain NUL bytes")
		}
		if len(arg) > maxCommandBytes-total {
			return errs.New(errs.InvalidArgument, "command exceeds %d byte argv limit", maxCommandBytes)
		}
		total += len(arg)
	}
	return nil
}

func (m *Manager) register(job *Job) {
	m.mu.Lock()
	if !m.closed && m.makeJobRoomLocked() {
		m.jobs[job.ID] = job
	}
	m.mu.Unlock()
	m.persist()
}

func (m *Manager) makeJobRoomLocked() bool {
	if len(m.jobs)+m.reservedJobs < maxJobsInMemory {
		return true
	}
	oldestID := ""
	var oldest time.Time
	for id, job := range m.jobs {
		job.mu.Lock()
		terminal := job.status.Terminal()
		ended := job.endedAt
		job.mu.Unlock()
		if terminal && (oldestID == "" || ended.Before(oldest)) {
			oldestID, oldest = id, ended
		}
	}
	if oldestID == "" {
		return false
	}
	delete(m.jobs, oldestID)
	return true
}

// registerWithQuota atomically enforces the per-agent concurrency quota (MA-3)
// and inserts the job. Holding m.mu across the count-and-insert is what makes it
// race-free: an unsynchronised check-then-insert lets two concurrent Starts on
// different sessions of the same agent both pass and both register, exceeding
// MaxJobsPerAgent. On rejection the job is NOT inserted and ParallelismExceeded
// is returned; the caller owns tearing down the already-started job.
func (m *Manager) publishStarted(job *Job) {
	m.publish(bus.Event{Type: bus.EvJobStarted, AgentID: job.sess.Owner, SessionID: job.SessionID,
		JobID: job.ID, Message: strings.Join(job.Command, " ")})
}

// autoAnswerWatch applies a policy's auto_answer rules to a job that is waiting
// for input (spec IN-2): only when the job is confirmed awaiting_input and the
// prompt tail matches a rule, each rule fires at most once.
func (m *Manager) autoAnswerWatch(job *Job, owner string) {
	if m.pol == nil {
		return
	}
	rules := m.pol.AutoAnswers(m.agentPolicies[owner])
	if len(rules) == 0 {
		return
	}
	go func() {
		answered := map[string]bool{}
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-job.Done():
				return
			case <-t.C:
				info := job.info()
				if info.Status != StatusAwaitingInput || info.Prompt == "" {
					continue
				}
				for _, rule := range rules {
					if rule.Match == "" || answered[rule.Match] {
						continue
					}
					if strings.Contains(info.Prompt, rule.Match) {
						answered[rule.Match] = true
						_ = job.sess.writeInput(job, []byte(rule.Send+"\n"))
						// The answer may be a secret (a configured passphrase), so it
						// must never reach the bus — which feeds the audit log,
						// off-box notifications and the dashboard. Surface only what
						// it matched, not the value sent. (PTY echo is already off, so
						// the value doesn't land in the output buffer either.)
						m.publish(bus.Event{Type: "auto_answer", AgentID: owner, JobID: job.ID,
							Message: "auto-answered prompt matching " + rule.Match, Data: map[string]any{"matched": rule.Match}})
					}
				}
			}
		}
	}()
}

// prepareWatchLocked reserves shutdown accountability for a job before it is
// exposed in the registry. The watcher waits for release so job.started always
// precedes job.finished, even when the command exits during Start. Caller holds
// m.mu, which serializes WaitGroup.Add with Shutdown's one-way closed transition.
func (m *Manager) prepareWatchLocked(job *Job) func() {
	ready := make(chan struct{})
	var releaseOnce sync.Once
	m.watchWG.Add(1)
	go func() {
		defer m.watchWG.Done()
		<-ready
		<-job.Done()
		info := job.info()
		m.publish(bus.Event{Type: bus.EvJobFinished, AgentID: job.sess.Owner, SessionID: job.SessionID,
			JobID: job.ID, Message: string(info.Status),
			Data: map[string]any{"status": info.Status, "exit_code": info.ExitCode, "reason": info.Reason}})
		m.persist()
	}()
	return func() { releaseOnce.Do(func() { close(ready) }) }
}

// enqueueConfirm parks a command awaiting human approval and returns the job in
// awaiting_confirmation. A timer denies it by default after the configured
// timeout (spec §18a: deny-by-default).
func (m *Manager) enqueueConfirm(owner string, sess *Session, command []string, mode string, dec policy.Result) (*Job, error) {
	job := newConfirmJob(sess, command, mode)
	cid := ids.New("cnf")
	job.setConfirmID(cid)
	pc := &pendingConfirm{ID: cid, Job: job, Owner: owner, Sess: sess, Argv: command, Mode: mode,
		Reason: dec.Reason, Matched: dec.Matched, Created: time.Now()}
	if err := m.registerPending(pc); err != nil {
		return nil, err
	}
	m.touchAgent(owner, func(a *AgentStat) { a.Jobs++; a.recordCommand(cmdString(command)) })
	if err := m.publish(bus.Event{Type: bus.EvConfirmRequested, AgentID: owner, SessionID: sess.ID, JobID: job.ID,
		Message: strings.Join(command, " "),
		Data:    map[string]any{"confirmation_id": cid, "matched": dec.Matched}}); err != nil {
		m.mu.Lock()
		delete(m.pending, cid)
		pc.resolved = true
		m.mu.Unlock()
		job.finalize(-1, StatusFailed, "audit log unavailable")
		m.persist()
		return nil, errs.New(errs.Internal, "audit log unavailable - refusing dangerous command: %v", err)
	}

	timeout := time.Duration(m.cfg.ConfirmTimeoutMS) * time.Millisecond
	if timeout > 0 {
		timer := time.AfterFunc(timeout, func() {
			_ = m.resolveConfirm(cid, false, "timed out", "system")
		})
		m.mu.Lock()
		if pc.resolved {
			timer.Stop()
		} else {
			pc.timer = timer
		}
		m.mu.Unlock()
	}
	return job, nil
}

func (m *Manager) registerPending(pc *pendingConfirm) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return managerClosedError()
	}
	if len(m.pending) >= maxPendingTotal {
		m.mu.Unlock()
		return errs.New(errs.ParallelismExceeded, "daemon reached its pending-confirmation limit (%d)", maxPendingTotal)
	}
	owned := 0
	for _, pending := range m.pending {
		if pending.Owner == pc.Owner {
			owned++
		}
	}
	if owned >= maxPendingPerOwner {
		m.mu.Unlock()
		return errs.New(errs.ParallelismExceeded, "agent %q reached its pending-confirmation limit (%d)", pc.Owner, maxPendingPerOwner)
	}
	if m.cfg.MaxJobsPerAgent > 0 && m.activeForAgentLocked(pc.Owner) >= m.cfg.MaxJobsPerAgent {
		m.mu.Unlock()
		return errs.New(errs.ParallelismExceeded, "agent %q reached its concurrent-job quota (%d)", pc.Owner, m.cfg.MaxJobsPerAgent)
	}
	if !m.makeJobRoomLocked() || len(m.jobs)+m.reservedJobs >= maxJobsInMemory {
		m.mu.Unlock()
		return errs.New(errs.ParallelismExceeded, "job registry reached its active/history limit (%d)", maxJobsInMemory)
	}
	m.jobs[pc.Job.ID] = pc.Job
	m.pending[pc.ID] = pc
	m.mu.Unlock()
	if err := m.persist(); err != nil {
		m.mu.Lock()
		if m.pending[pc.ID] == pc {
			delete(m.pending, pc.ID)
		}
		if m.jobs[pc.Job.ID] == pc.Job {
			delete(m.jobs, pc.Job.ID)
		}
		m.mu.Unlock()
		return errs.New(errs.Internal, "persist pending confirmation: %v", err)
	}
	m.mu.Lock()
	closed := m.closed || m.pending[pc.ID] != pc
	m.mu.Unlock()
	if closed {
		return managerClosedError()
	}
	return nil
}

func (m *Manager) resolveConfirm(cid string, approved bool, reason, by string) error {
	m.mu.Lock()
	pc := m.pending[cid]
	if pc == nil || pc.resolved {
		m.mu.Unlock()
		return errs.New(errs.NotFound, "confirmation %s not found or already resolved", cid)
	}
	if approved && m.cfg.MaxJobsPerAgent > 0 {
		activeOthers := 0
		for _, job := range m.jobs {
			if job == pc.Job || job.sess.Owner != pc.Owner {
				continue
			}
			job.mu.Lock()
			terminal := job.status.Terminal()
			job.mu.Unlock()
			if !terminal {
				activeOthers++
			}
		}
		if activeOthers >= m.cfg.MaxJobsPerAgent {
			m.mu.Unlock()
			return errs.New(errs.ParallelismExceeded, "agent %q reached its concurrent-job quota (%d)", pc.Owner, m.cfg.MaxJobsPerAgent)
		}
	}
	if approved && m.auditOK != nil && !m.auditOK() {
		m.mu.Unlock()
		return errs.New(errs.Internal, "audit log unavailable - refusing dangerous command (fail-closed)")
	}
	if approved {
		if pc.Mode == ModeBackground {
			if m.cfg.MaxBackgroundJobs > 0 && m.activeBackground()+m.reservedBackground >= m.cfg.MaxBackgroundJobs {
				m.mu.Unlock()
				return errs.New(errs.ParallelismExceeded, "max background jobs (%d) reached", m.cfg.MaxBackgroundJobs)
			}
			m.reservedBackground++
		} else {
			if m.cfg.MaxForegroundJobs > 0 && m.activeForeground()+m.reservedForeground >= m.cfg.MaxForegroundJobs {
				m.mu.Unlock()
				return errs.New(errs.ParallelismExceeded, "max foreground jobs (%d) reached", m.cfg.MaxForegroundJobs)
			}
			m.reservedForeground++
		}
		// Unlike ordinary starts, confirmation jobs are already present in m.jobs.
		// Exclude this job from the live scan until its reservation is released,
		// otherwise activation briefly counts the same quota slot twice.
		m.reservedQuotaJobs[pc.Job.ID] = struct{}{}
	}
	if pc.timer != nil {
		pc.timer.Stop()
	}
	pc.resolved = true
	delete(m.pending, cid)
	var releaseWatch func()
	if approved {
		releaseWatch = m.prepareWatchLocked(pc.Job)
	}
	m.mu.Unlock()

	if approved {
		released := false
		defer func() {
			if !released {
				releaseWatch()
			}
		}()
		defer func() {
			m.mu.Lock()
			delete(m.reservedQuotaJobs, pc.Job.ID)
			if pc.Mode == ModeBackground {
				m.reservedBackground--
			} else {
				m.reservedForeground--
			}
			m.mu.Unlock()
		}()
		// Persist the human authorization before starting the dangerous command.
		// A health probe alone has a race with the next disk write; synchronous
		// delivery closes that window.
		if err := m.publish(bus.Event{Type: bus.EvConfirmResolved, JobID: pc.Job.ID, AgentID: pc.Owner,
			SessionID: pc.Sess.ID,
			Data:      map[string]any{"confirmation_id": cid, "approved": true, "by": by}}); err != nil {
			pc.Job.finalize(-1, StatusFailed, "audit log unavailable during approval")
			m.persist()
			return errs.New(errs.Internal, "audit log unavailable - refusing dangerous command: %v", err)
		}
		if err := pc.Sess.startJob(pc.Job, quoteArgv(pc.Argv)); err != nil {
			pc.Job.finalize(-1, StatusFailed, "exec after approve: "+err.Error())
			return err
		}
		if pc.Mode == ModeBackground {
			// Keep the reservation until the approved job is installed and visible
			// in the background pool.
			pc.Job.markBackground()
		}
		m.publishStarted(pc.Job)
		releaseWatch()
		released = true
		m.autoAnswerWatch(pc.Job, pc.Owner)
		return nil
	}
	pc.Job.finalize(-1, StatusFailed, "confirmation "+reason+" (by "+by+")")
	persistErr := m.persist()
	if err := m.publish(bus.Event{Type: bus.EvConfirmResolved, JobID: pc.Job.ID, AgentID: pc.Owner,
		Data: map[string]any{"confirmation_id": cid, "approved": false, "by": by, "reason": reason}}); err != nil {
		if persistErr != nil {
			return errs.New(errs.Internal, "command denied but audit recording and terminal state persistence failed: audit: %v; persistence: %v", err, persistErr)
		}
		return errs.New(errs.Internal, "command denied but audit recording failed: %v", err)
	}
	if persistErr != nil {
		return errs.New(errs.Internal, "command denied but terminal state persistence failed: %v", persistErr)
	}
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

// capChunk trims chunk to at most n bytes, keeping the next sequential (head)
// portion. Callers advance their cursor only by the returned bytes, so the
// remainder can be fetched by the next poll instead of being silently skipped.
func capChunk(chunk []byte, n int) ([]byte, bool) {
	if n <= 0 || len(chunk) <= n {
		return chunk, false
	}
	return chunk[:n], true
}

// RunResult is the result of a blocking exec_run.
type RunResult struct {
	Info
	Stdout     string `json:"stdout"`
	NextCursor string `json:"next_cursor"`
	Truncated  bool   `json:"truncated"`
	HasMore    bool   `json:"has_more,omitempty"`
	WaitedMS   int64  `json:"waited_ms,omitempty"`  // how long exec_run actually waited
	TimeoutMS  int64  `json:"timeout_ms,omitempty"` // the wait budget it applied
}

// Run starts a command and waits up to timeout for completion. If it does not
// finish in time it returns with the current (running/backgrounded) status and
// the output so far (spec EX-7).
func (m *Manager) Run(owner, sessionID string, command []string, mode string, timeoutMS int) (*RunResult, error) {
	if timeoutMS < 0 || timeoutMS > maxExecWaitMS {
		return nil, errs.New(errs.InvalidArgument, "timeout_ms must be in 0..%d", maxExecWaitMS)
	}
	job, err := m.Start(owner, sessionID, command, mode)
	if err != nil {
		return nil, err
	}
	// Choose how long to wait before handing control back (spec LR-1/LR-2):
	//   - background mode: short grace to capture startup output, then return;
	//   - auto mode + daemon signature (dev server/watcher): same short grace;
	//   - otherwise: wait up to the adaptive (per-class) or explicit timeout.
	// On timeout the job is NOT killed — it is reported as backgrounded so the
	// agent gets control back and can poll/kill it.
	autoBg := mode == ModeAuto || mode == ""
	var wait time.Duration
	reason := ""
	switch {
	case mode == ModeBackground:
		wait = 250 * time.Millisecond
		reason = "started in background"
	case autoBg && isDaemon(command):
		// A daemon-looking command is auto-backgrounded after a short grace — but
		// honor an EXPLICIT timeout_ms if the agent set one (it would otherwise be
		// silently discarded for anything matching the daemon heuristic).
		if timeoutMS > 0 {
			wait = time.Duration(timeoutMS) * time.Millisecond
			reason = "still running after timeout; left running in background"
		} else {
			wait = 1500 * time.Millisecond
			reason = "auto-backgrounded (long-running command)"
		}
	default:
		if timeoutMS <= 0 {
			timeoutMS = m.classTimeout(command)
		}
		wait = time.Duration(timeoutMS) * time.Millisecond
		reason = "still running after timeout; left running in background"
	}
	waitStart := time.Now()
	// Don't burn the wait budget once the job is parked for human/agent action:
	// a confirm-gated command is already awaiting_confirmation the instant Start
	// returns, and a running command can flip to awaiting_input mid-wait once it
	// hits an interactive prompt. Either way, hand control back immediately with
	// the TRUE status instead of blocking out the full timeout and then reporting
	// "backgrounded" — which would hide that a human needs to approve it, or that
	// exec_write is needed to answer a prompt (spec IN-1/§18a).
	parked := func() bool {
		s := job.info().Status
		return s == StatusAwaitingConfirmation || s == StatusAwaitingInput
	}
	if !parked() {
		deadline := time.NewTimer(wait)
		tick := time.NewTicker(150 * time.Millisecond)
	waitLoop:
		for {
			select {
			case <-job.Done():
				break waitLoop
			case <-deadline.C:
				break waitLoop
			case <-tick.C:
				if parked() {
					break waitLoop
				}
			}
		}
		deadline.Stop()
		tick.Stop()
	}
	chunk, next, gap, capped := job.clean.ReadFromLimit(0, m.maxOutputBytes())
	info := job.info()
	if !info.Status.Terminal() && info.Status != StatusAwaitingConfirmation && info.Status != StatusAwaitingInput {
		info.Status = StatusBackgrounded
		reclassified := m.markBackgroundWithinQuota(job)
		if info.Reason == "" {
			info.Reason = reason
		}
		// Reclassify off the foreground quota from this point on (mirrors the
		// explicit mode=background case in Start): a daemon-heuristic or
		// timed-out-but-left-running job no longer occupies a foreground slot.
		if !reclassified {
			info.Reason += "; background quota is full, so this job still occupies a foreground slot"
		}
	}
	return &RunResult{
		Info:       info,
		Stdout:     string(chunk),
		NextCursor: output.EncodeCursor(next),
		Truncated:  gap || capped,
		HasMore:    capped,
		WaitedMS:   time.Since(waitStart).Milliseconds(),
		TimeoutMS:  wait.Milliseconds(),
	}, nil
}

// PollResult is returned by exec_poll.
type PollResult struct {
	Info
	StdoutChunk string `json:"stdout_chunk"`
	NextCursor  string `json:"next_cursor"`
	Gap         bool   `json:"gap,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	HasMore     bool   `json:"has_more,omitempty"`
	OutputHeld  bool   `json:"output_held,omitempty"`
}

// Poll returns incremental output for the agent (respects an output hold).
// owner scopes the lookup (MA-2); pass "" for the trusted/human path.
func (m *Manager) Poll(owner, jobID, cursor string) (*PollResult, error) {
	return m.PollFor(owner, jobID, cursor, false)
}

// PollWait is Poll with an optional long-poll: if there is nothing new yet and
// waitMS > 0, it blocks (capped at 30s) until new output arrives, the job goes
// terminal, it starts awaiting input, or the budget elapses — then returns the
// latest. This replaces the agent's busy poll-sleep-poll loop (which the model
// often gets wrong) with one call. waitMS <= 0 is the old non-blocking behavior.
func (m *Manager) PollWait(owner, jobID, cursor string, waitMS int) (*PollResult, error) {
	res, err := m.PollFor(owner, jobID, cursor, false)
	if err != nil || waitMS <= 0 {
		return res, err
	}
	if res.StdoutChunk != "" || res.Status.Terminal() || res.AwaitingInput || res.OutputHeld {
		return res, nil
	}
	if waitMS > 30_000 {
		waitMS = 30_000
	}
	job, err := m.getJob(owner, jobID)
	if err != nil {
		return res, nil
	}
	deadline := time.NewTimer(time.Duration(waitMS) * time.Millisecond)
	defer deadline.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			return m.PollFor(owner, jobID, cursor, false)
		case <-job.Done():
			return m.PollFor(owner, jobID, cursor, false)
		case <-tick.C:
			r, e := m.PollFor(owner, jobID, cursor, false)
			if e != nil {
				return r, e
			}
			if r.StdoutChunk != "" || r.Status.Terminal() || r.AwaitingInput || r.OutputHeld {
				return r, nil
			}
		}
	}
}

// PollFor returns incremental output from the cursor onward plus current status.
// When human is false and the job's output is held, no new bytes are returned
// (the agent is paused); the human dashboard passes human=true to always stream
// and owner="" to see any agent's job. The returned chunk is capped at
// maxOutputBytes per call (keeping sequential order) so one poll can't dump an
// unbounded amount into the caller's context; HasMore tells the caller to fetch
// the remainder from NextCursor.
func (m *Manager) PollFor(owner, jobID, cursor string, human bool) (*PollResult, error) {
	job, err := m.getJob(owner, jobID)
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
	chunk, next, gap, capped := job.clean.ReadFromLimit(off, m.maxOutputBytes())
	return &PollResult{
		Info:        job.info(),
		StdoutChunk: string(chunk),
		NextCursor:  output.EncodeCursor(next),
		Gap:         gap,
		Truncated:   capped,
		HasMore:     capped,
	}, nil
}

// TailResult is returned by logs_tail.
type TailResult struct {
	Lines      string `json:"lines"`
	NextCursor string `json:"next_cursor"`
	Gap        bool   `json:"gap,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	HasMore    bool   `json:"has_more,omitempty"`
	OutputHeld bool   `json:"output_held,omitempty"`
}

// Tail returns output from the cursor (or, if empty, the whole retained
// stream). It honors an output hold exactly like PollFor (human bypasses it,
// same as the dashboard's live view) so a human-paused agent can't read the
// withheld stream through this tool instead of exec_poll.
func (m *Manager) Tail(owner, jobID, cursor string, human bool) (*TailResult, error) {
	job, err := m.getJob(owner, jobID)
	if err != nil {
		return nil, err
	}
	off, err := output.DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	if _, ho := job.holds(); ho && !human {
		return &TailResult{NextCursor: cursor, OutputHeld: true}, nil
	}
	chunk, next, gap, capped := job.clean.ReadFromLimit(off, m.maxOutputBytes())
	return &TailResult{
		Lines:      string(chunk),
		NextCursor: output.EncodeCursor(next),
		Gap:        gap,
		Truncated:  capped,
		HasMore:    capped,
	}, nil
}

// Write sends input to a job's PTY. If secret is true the value is registered
// for redaction and is never echoed/logged (spec IN-3/§3a). human=true marks
// input typed by a person at the dashboard/TUI; when a job's input is held
// (human takeover), agent writes (human=false) are rejected. owner scopes the
// job lookup (MA-2); pass "" for the trusted/human path.
//
// The job must still be the session's CURRENT command: once a job exits, its id
// stays valid (for polling history) but the session shell has moved on to an
// idle prompt, so writing to it any longer would type the input directly into
// that idle shell — executed as a command, unaudited and unchecked by policy —
// instead of answering the (now-gone) prompt it was meant for.
func (m *Manager) Write(owner, jobID, input string, appendNewline, secret, human bool) error {
	job, err := m.getJob(owner, jobID)
	if err != nil {
		return err
	}
	if job.sess.currentJob() != job {
		return errs.New(errs.NotFound, "job %s is not currently running", jobID)
	}
	if hi, _ := job.holds(); hi && !human {
		return errs.New(errs.DeniedByPolicy, "input is held by a human operator")
	}
	// PTY input is shared with the persistent shell. If an agent writes while the
	// foreground process is not actually waiting for input, those bytes can remain
	// queued and be interpreted by the shell as a new, unaudited command after the
	// job exits. Operators intentionally retain unrestricted terminal control, but
	// untrusted agents may only answer a prompt the engine has confirmed.
	if !human && !job.info().AwaitingInput {
		return errs.New(errs.DeniedByPolicy, "job %s is not awaiting input", jobID)
	}
	inputBytes := len(input)
	if appendNewline {
		inputBytes++
	}
	if inputBytes > maxSessionInputBytes {
		return errs.New(errs.InvalidArgument, "input exceeds %d byte limit", maxSessionInputBytes)
	}
	if secret {
		if err := m.redactor.AddLiteral(input); err != nil {
			return errs.New(errs.Internal, "cannot safely register secret for redaction: %v", err)
		}
	}
	data := []byte(input)
	if appendNewline {
		data = append(data, '\n')
	}
	return job.sess.writeInput(job, data)
}

// Hold sets the human-intervention flags for a job (nil = unchanged). Used by
// the dashboard/CLI so a person can take over input and/or pause the output the
// agent receives, while still watching the live stream themselves. Dashboard-
// only (not exposed as an MCP tool), so it sees any agent's job.
func (m *Manager) Hold(jobID string, input, output *bool) error {
	job, err := m.getJob("", jobID)
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
// owner scopes the job lookup (MA-2); pass "" for the trusted/human path.
func (m *Manager) Signal(owner, jobID, signal string) error {
	job, err := m.getJob(owner, jobID)
	if err != nil {
		return err
	}
	sess := job.sess
	if sess.currentJob() != job {
		return errs.New(errs.NotFound, "job %s is not currently running", jobID)
	}
	job.requestKill(signal)
	return sess.shell.Signal(signal)
}

// Kill force-terminates a job (SIGKILL to its process group).
func (m *Manager) Kill(owner, jobID string) error {
	job, err := m.getJob(owner, jobID)
	if err != nil {
		return err
	}
	info := job.info()
	if info.Status == StatusAwaitingConfirmation && info.ConfirmationID != "" {
		m.mu.Lock()
		pending := m.pending[info.ConfirmationID]
		cancelled := false
		if pending != nil && pending.Job == job && !pending.resolved {
			pending.resolved = true
			if pending.timer != nil {
				pending.timer.Stop()
			}
			delete(m.pending, info.ConfirmationID)
			cancelled = true
		}
		m.mu.Unlock()
		if cancelled {
			job.finalize(-1, StatusKilled, "confirmation cancelled")
			m.persist()
			if err := m.publish(bus.Event{Type: bus.EvConfirmResolved, JobID: job.ID, AgentID: job.sess.Owner,
				SessionID: job.SessionID,
				Data:      map[string]any{"confirmation_id": info.ConfirmationID, "approved": false, "by": "kill", "reason": "cancelled"}}); err != nil {
				return errs.New(errs.Internal, "confirmation was cancelled but audit recording failed: %v", err)
			}
			return nil
		}
	}
	return m.Signal(owner, jobID, "SIGKILL")
}

// ListJobs returns job snapshots filtered by active|recent|all, scoped to owner
// (MA-2): a non-empty owner sees only its own jobs (including its own recovered
// jobs across a daemon restart). owner == "" is the trusted/human path and sees
// every agent's jobs, as the dashboard needs.
func (m *Manager) ListJobs(owner, filter string) []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	type row struct {
		at   time.Time
		info Info
	}
	rows := []row{}
	keep := func(at time.Time, info Info) {
		if owner != "" && info.Owner != owner {
			return
		}
		if filter == "active" && info.Status.Terminal() {
			return
		}
		rows = append(rows, row{at, info})
	}
	for _, j := range m.jobs {
		keep(j.createdAt, j.info()) // createdAt is immutable after newJob
	}
	for _, in := range m.recovered {
		keep(time.Time{}, in) // recovered jobs have no live timestamp — sort oldest
	}
	// Newest first, so a default (capped) listing shows what just happened —
	// deterministic order instead of Go's randomized map iteration.
	sort.Slice(rows, func(i, j int) bool { return rows[i].at.After(rows[j].at) })
	out := make([]Info, len(rows))
	for i, r := range rows {
		out[i] = r.info
	}
	return out
}

// ListSessions returns all session snapshots for trusted/operator callers.
func (m *Manager) ListSessions() []SessionInfo { return m.ListSessionsFor("") }

// ListSessionsFor returns session snapshots scoped to owner. A non-empty owner
// never learns that another agent's session exists.
func (m *Manager) ListSessionsFor(owner string) []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []SessionInfo{}
	for _, s := range m.sessions {
		if owner != "" && s.Owner != owner {
			continue
		}
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

// CloseSession closes and removes a session, scoped to owner (MA-2): a
// non-empty owner can only close its own session. owner == "" is the trusted/
// human path (dashboard/CLI/internal callers).
func (m *Manager) CloseSession(owner, id string) error {
	m.mu.Lock()
	sess := m.sessions[id]
	if sess == nil || (owner != "" && sess.Owner != owner) {
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
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	pending := make([]*pendingConfirm, 0, len(m.pending))
	for _, confirmation := range m.pending {
		if confirmation.timer != nil {
			confirmation.timer.Stop()
		}
		confirmation.resolved = true
		pending = append(pending, confirmation)
	}
	m.sessions = map[string]*Session{}
	m.defaults = map[string]string{}
	m.pending = map[string]*pendingConfirm{}
	forwards := m.forwards
	m.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
	for _, confirmation := range pending {
		confirmation.Job.finalize(-1, StatusFailed, "daemon shutting down")
	}
	if len(pending) > 0 {
		_ = m.persist()
	}
	// Every watcher was reserved under m.mu before shutdown set closed, so no Add
	// can race this Wait. Keep reliable sinks alive until terminal events and their
	// registry writes have completed.
	m.watchWG.Wait()
	shutdownForwardOps(forwards)
}

// SessionTarget returns a session's target ("local" or a server name) and
// whether the session exists. An empty id (the per-agent default session) is
// reported as not-found so callers treat it as local. Used to keep file_read/
// file_write from operating on the wrong host (e.g. local FS while the agent
// believes it is in a remote session).
func (m *Manager) SessionTarget(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return "", false
	}
	return s.Target, true
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
	HasMore    bool   `json:"has_more,omitempty"`
}

// SessionTail returns the session's continuous terminal output from the cursor
// onward (the whole shell, across all jobs) — what the dashboard streams as one
// live terminal for the session.
func (m *Manager) SessionTail(sessionID, cursor string) (*SessionStreamResult, error) {
	return m.SessionTailFor("", sessionID, cursor)
}

// SessionTailFor is the owner-scoped session stream used by agent transports.
func (m *Manager) SessionTailFor(owner, sessionID, cursor string) (*SessionStreamResult, error) {
	s, err := m.getSessionFor(owner, sessionID)
	if err != nil {
		return nil, err
	}
	off, err := output.DecodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	chunk, next, gap, hasMore := s.clean.ReadFromLimit(off, m.maxOutputBytes())
	return &SessionStreamResult{
		Chunk:      string(chunk),
		NextCursor: output.EncodeCursor(next),
		Gap:        gap,
		Closed:     s.isClosed(),
		HasMore:    hasMore,
	}, nil
}

// SessionWriteInput sends operator input directly to a session's shell PTY.
// If the current foreground job's input is held, agent input (human=false) is
// rejected; operator input (human=true) always goes through.
func (m *Manager) SessionWriteInput(sessionID, input string, appendNewline, human bool) error {
	return m.SessionWriteInputFor("", sessionID, input, appendNewline, human)
}

// SessionWriteInputFor scopes direct PTY input to the authenticated owner.
func (m *Manager) SessionWriteInputFor(owner, sessionID, input string, appendNewline, human bool) error {
	s, err := m.getSessionFor(owner, sessionID)
	if err != nil {
		return err
	}
	cur := s.currentJob()
	if cur == nil {
		if !human {
			return errs.New(errs.NotFound, "session has no running job")
		}
	}
	if cur != nil {
		if hi, _ := cur.holds(); hi && !human {
			return errs.New(errs.DeniedByPolicy, "input is held by a human operator")
		}
	}
	inputBytes := len(input)
	if appendNewline {
		inputBytes++
	}
	if inputBytes > maxSessionInputBytes {
		return errs.New(errs.InvalidArgument, "input exceeds %d byte limit", maxSessionInputBytes)
	}
	data := []byte(input)
	if appendNewline {
		data = append(data, '\n')
	}
	if cur == nil {
		return s.writeCurrent(nil, data, interactiveWriteTimeout, false)
	}
	return s.writeInput(cur, data)
}

func (m *Manager) getSessionFor(owner, id string) (*Session, error) {
	s, err := m.getSession(id)
	if err != nil || (owner != "" && s.Owner != owner) {
		return nil, errs.New(errs.NotFound, "session %s not found", id)
	}
	return s, nil
}

// getJob resolves a job by id, scoped to owner (MA-2 job isolation): when owner
// is non-empty, a job belonging to a different agent is reported as not-found
// (not a permission error) so its existence isn't leaked. owner == "" is the
// trusted/human path (dashboard, CLI, internal callers like the GC reaper) and
// sees every job.
func (m *Manager) getJob(owner, id string) (*Job, error) {
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job == nil {
		return nil, errs.New(errs.NotFound, "job %s not found", id)
	}
	if owner != "" && job.sess.Owner != owner {
		return nil, errs.New(errs.NotFound, "job %s not found", id)
	}
	return job, nil
}
