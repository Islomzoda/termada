// Package mission groups agent execution into durable, evidence-backed missions.
package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/ids"
)

const (
	StatusPlanned        = "planned"
	StatusRunning        = "running"
	StatusNeedsAttention = "needs_attention"
	StatusInterrupted    = "interrupted"
	StatusSucceeded      = "succeeded"
	StatusFailed         = "failed"
	StatusCancelled      = "cancelled"

	StepPending = "pending"
	StepRunning = "running"
	StepPassed  = "passed"
	StepFailed  = "failed"
	StepSkipped = "skipped"

	maxMissions       = 128
	maxSteps          = 24
	maxEvents         = 512
	maxTitleBytes     = 160
	maxGoalBytes      = 4000
	maxSummaryBytes   = 8000
	maxStepTitleBytes = 300
	maxNoteBytes      = 2000
)

// Step is one GPT/Codex-authored plan item. Passed steps must reference a real,
// successful job from the mission session.
type Step struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Status      string     `json:"status"`
	JobID       string     `json:"job_id,omitempty"`
	Note        string     `json:"note,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Event is a bounded, redacted projection of a runtime bus event. It stores
// only evidence fields needed by the dashboard/report, never terminal output.
type Event struct {
	Sequence       uint64    `json:"sequence,omitempty"`
	Time           time.Time `json:"time"`
	Type           string    `json:"type"`
	JobID          string    `json:"job_id,omitempty"`
	Message        string    `json:"message,omitempty"`
	Status         string    `json:"status,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	ConfirmationID string    `json:"confirmation_id,omitempty"`
	Approved       *bool     `json:"approved,omitempty"`
	By             string    `json:"by,omitempty"`
}

// Mission is the durable unit of work shown in Mission Control.
type Mission struct {
	ID          string     `json:"id"`
	Owner       string     `json:"owner"`
	Title       string     `json:"title"`
	Goal        string     `json:"goal"`
	Target      string     `json:"target"`
	Workspace   string     `json:"workspace,omitempty"`
	SessionID   string     `json:"session_id"`
	SessionIDs  []string   `json:"session_ids"`
	Status      string     `json:"status"`
	Summary     string     `json:"summary,omitempty"`
	Steps       []Step     `json:"steps"`
	Events      []Event    `json:"events"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Summary is the compact mission list/dashboard bootstrap representation.
type Summary struct {
	ID          string     `json:"id"`
	Owner       string     `json:"owner"`
	Title       string     `json:"title"`
	Goal        string     `json:"goal"`
	Target      string     `json:"target"`
	Workspace   string     `json:"workspace,omitempty"`
	SessionID   string     `json:"session_id"`
	Status      string     `json:"status"`
	StepsPassed int        `json:"steps_passed"`
	StepsTotal  int        `json:"steps_total"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// CreateRequest is the MCP/control-plane input for a new mission.
type CreateRequest struct {
	Title     string   `json:"title"`
	Goal      string   `json:"goal"`
	Target    string   `json:"target"`
	Workspace string   `json:"workspace"`
	Plan      []string `json:"plan"`
}

// UpdateRequest changes a plan step or finishes a mission.
type UpdateRequest struct {
	StepID     string `json:"step_id"`
	StepStatus string `json:"step_status"`
	JobID      string `json:"job_id"`
	Note       string `json:"note"`
	Status     string `json:"status"`
	Summary    string `json:"summary"`
}

// Service persists missions and correlates runtime events by session id.
type Service struct {
	path     string
	mgr      *engine.Manager
	publish  func(bus.Event) error
	redact   func(string) string
	mu       sync.Mutex
	missions map[string]*Mission
	bySess   map[string]string
}

type diskState struct {
	Version  int        `json:"version"`
	Missions []*Mission `json:"missions"`
}

// New loads the mission store. Active missions become interrupted because
// Termada sessions are deliberately not recoverable across daemon restarts.
func New(path string, mgr *engine.Manager, publish func(bus.Event) error, redact func(string) string) (*Service, error) {
	if mgr == nil {
		return nil, fmt.Errorf("mission service requires an engine manager")
	}
	s := &Service{path: path, mgr: mgr, publish: publish, redact: redact, missions: map[string]*Mission{}, bySess: map[string]string{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	changed := false
	now := time.Now().UTC()
	for _, mission := range s.missions {
		if !terminalStatus(mission.Status) {
			mission.Status = StatusInterrupted
			mission.UpdatedAt = now
			mission.Events = appendBounded(mission.Events, Event{Time: now, Type: "mission.interrupted", Message: "daemon restarted; create a fresh session before continuing"})
			changed = true
		}
	}
	if changed {
		if err := s.persistLocked(); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	s.reindexLocked()
	s.mu.Unlock()
	return s, nil
}

// Create starts a dedicated persistent session and records an auditable mission.
func (s *Service) Create(owner string, req CreateRequest) (*Mission, error) {
	if err := validateCreate(req); err != nil {
		return nil, err
	}
	target := req.Target
	if target == "" {
		target = "local"
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = deriveTitle(req.Goal)
	}
	sess, err := s.mgr.CreateSessionWithWorkspace(owner, target, "shell", strings.TrimSpace(req.Workspace))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	m := &Mission{
		ID: ids.New("msn"), Owner: owner, Title: title, Goal: strings.TrimSpace(req.Goal), Target: target,
		Workspace: strings.TrimSpace(req.Workspace), SessionID: sess.ID, SessionIDs: []string{sess.ID}, Status: StatusPlanned,
		CreatedAt: now, UpdatedAt: now, Steps: make([]Step, 0, len(req.Plan)), Events: []Event{},
	}
	for i, title := range req.Plan {
		m.Steps = append(m.Steps, Step{ID: fmt.Sprintf("step_%d", i+1), Title: strings.TrimSpace(title), Status: StepPending, UpdatedAt: now})
	}
	m.Events = append(m.Events, Event{Time: now, Type: "mission.created", Message: m.Goal})

	s.mu.Lock()
	if len(s.missions) >= maxMissions {
		s.pruneLocked()
	}
	if len(s.missions) >= maxMissions {
		s.mu.Unlock()
		_ = s.mgr.CloseSession(owner, sess.ID)
		return nil, errs.New(errs.ParallelismExceeded, "mission limit (%d) reached", maxMissions)
	}
	s.missions[m.ID] = m
	s.bySess[m.SessionID] = m.ID
	if err := s.persistLocked(); err != nil {
		delete(s.missions, m.ID)
		delete(s.bySess, m.SessionID)
		s.mu.Unlock()
		_ = s.mgr.CloseSession(owner, sess.ID)
		return nil, errs.New(errs.Internal, "persist mission: %v", err)
	}
	out := cloneMission(m)
	s.mu.Unlock()
	if err := s.emit(bus.Event{Type: "mission.created", AgentID: owner, SessionID: m.SessionID, Message: m.Title, Data: map[string]any{"mission_id": m.ID, "target": m.Target}}); err != nil {
		s.mu.Lock()
		delete(s.missions, m.ID)
		delete(s.bySess, m.SessionID)
		_ = s.persistLocked()
		s.mu.Unlock()
		_ = s.mgr.CloseSession(owner, sess.ID)
		return nil, errs.New(errs.Internal, "mission created but audit recording failed: %v", err)
	}
	return out, nil
}

// Resume replaces the lost session of an interrupted mission while preserving
// its plan and prior evidence.
func (s *Service) Resume(owner, id string) (*Mission, error) {
	s.mu.Lock()
	m, err := s.getLocked(owner, id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if m.Status != StatusInterrupted {
		s.mu.Unlock()
		return nil, errs.New(errs.InvalidArgument, "mission %s is %s, not interrupted", id, m.Status)
	}
	target, workspace := m.Target, m.Workspace
	s.mu.Unlock()

	sess, err := s.mgr.CreateSessionWithWorkspace(owner, target, "shell", workspace)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	s.mu.Lock()
	m, err = s.getLocked(owner, id)
	if err != nil || m.Status != StatusInterrupted {
		s.mu.Unlock()
		_ = s.mgr.CloseSession(owner, sess.ID)
		if err != nil {
			return nil, err
		}
		return nil, errs.New(errs.InvalidArgument, "mission is no longer interrupted")
	}
	before := cloneMission(m)
	delete(s.bySess, m.SessionID)
	m.SessionID = sess.ID
	m.SessionIDs = append(m.SessionIDs, sess.ID)
	m.Status = StatusRunning
	m.UpdatedAt = now
	m.Events = appendBounded(m.Events, Event{Time: now, Type: "mission.resumed", Message: "continued in a fresh session"})
	s.bySess[m.SessionID] = m.ID
	if err := s.persistLocked(); err != nil {
		s.missions[id] = before
		s.reindexLocked()
		s.mu.Unlock()
		_ = s.mgr.CloseSession(owner, sess.ID)
		return nil, errs.New(errs.Internal, "persist resumed mission: %v", err)
	}
	out := cloneMission(m)
	s.mu.Unlock()
	if err := s.emit(bus.Event{Type: "mission.resumed", AgentID: owner, SessionID: sess.ID, Message: m.Title, Data: map[string]any{"mission_id": m.ID}}); err != nil {
		s.mu.Lock()
		s.missions[id] = before
		s.reindexLocked()
		_ = s.persistLocked()
		s.mu.Unlock()
		_ = s.mgr.CloseSession(owner, sess.ID)
		return nil, errs.New(errs.Internal, "mission resumed but audit recording failed: %v", err)
	}
	return out, nil
}

// Update records step evidence and/or a terminal outcome.
func (s *Service) Update(owner, id string, req UpdateRequest) (*Mission, error) {
	if len(req.Note) > maxNoteBytes || len(req.Summary) > maxSummaryBytes {
		return nil, errs.New(errs.InvalidArgument, "mission note or summary is too large")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	m, err := s.getLocked(owner, id)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if terminalStatus(m.Status) {
		s.mu.Unlock()
		return nil, errs.New(errs.InvalidArgument, "mission %s is already %s", id, m.Status)
	}
	before := cloneMission(m)
	if req.StepID != "" || req.StepStatus != "" || req.JobID != "" {
		if err := s.updateStepLocked(m, req, now); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	if strings.TrimSpace(req.Summary) != "" {
		m.Summary = strings.TrimSpace(req.Summary)
	}
	if req.Status != "" {
		if err := s.finishLocked(m, req.Status, now); err != nil {
			s.missions[id] = before
			s.reindexLocked()
			s.mu.Unlock()
			return nil, err
		}
	}
	if req.StepID == "" && req.Status == "" && strings.TrimSpace(req.Summary) == "" {
		s.mu.Unlock()
		return nil, errs.New(errs.InvalidArgument, "update must change a step, summary, or mission status")
	}
	m.UpdatedAt = now
	if err := s.persistLocked(); err != nil {
		s.missions[id] = before
		s.reindexLocked()
		s.mu.Unlock()
		return nil, errs.New(errs.Internal, "persist mission update: %v", err)
	}
	out := cloneMission(m)
	s.mu.Unlock()
	data := map[string]any{"mission_id": id}
	if req.StepID != "" {
		data["step_id"] = req.StepID
		data["step_status"] = req.StepStatus
		data["job_id"] = req.JobID
	}
	if req.Status != "" {
		data["status"] = req.Status
	}
	if err := s.emit(bus.Event{Type: "mission.updated", AgentID: owner, SessionID: out.SessionID, Message: out.Title, Data: data}); err != nil {
		s.mu.Lock()
		s.missions[id] = before
		s.reindexLocked()
		_ = s.persistLocked()
		s.mu.Unlock()
		return nil, errs.New(errs.Internal, "mission updated but audit recording failed: %v", err)
	}
	return out, nil
}

func (s *Service) updateStepLocked(m *Mission, req UpdateRequest, now time.Time) error {
	if req.StepID == "" || req.StepStatus == "" {
		return errs.New(errs.InvalidArgument, "step_id and step_status are required together")
	}
	var step *Step
	for i := range m.Steps {
		if m.Steps[i].ID == req.StepID {
			step = &m.Steps[i]
			break
		}
	}
	if step == nil {
		return errs.New(errs.NotFound, "mission step %q not found", req.StepID)
	}
	if !validStepStatus(req.StepStatus) {
		return errs.New(errs.InvalidArgument, "invalid step status %q", req.StepStatus)
	}
	if req.StepStatus == StepPassed {
		if req.JobID == "" {
			return errs.New(errs.InvalidArgument, "passed steps require job_id evidence")
		}
		info, ok := s.jobForMission(m, req.JobID)
		if !ok {
			return errs.New(errs.NotFound, "job %q is not part of this mission session", req.JobID)
		}
		if info.Status != engine.StatusExited || info.ExitCode == nil || *info.ExitCode != 0 {
			exitCode := -1
			if info.ExitCode != nil {
				exitCode = *info.ExitCode
			}
			return errs.New(errs.InvalidArgument, "job %s is %s with exit_code=%d; passed evidence requires exited with exit_code=0", req.JobID, info.Status, exitCode)
		}
	}
	if req.JobID != "" && req.StepStatus != StepPassed {
		if _, ok := s.jobForMission(m, req.JobID); !ok {
			return errs.New(errs.NotFound, "job %q is not part of this mission session", req.JobID)
		}
	}
	step.Status = req.StepStatus
	step.JobID = req.JobID
	step.Note = strings.TrimSpace(req.Note)
	step.UpdatedAt = now
	if req.StepStatus == StepPassed || req.StepStatus == StepFailed || req.StepStatus == StepSkipped {
		completed := now
		step.CompletedAt = &completed
	} else {
		step.CompletedAt = nil
	}
	m.Events = appendBounded(m.Events, Event{Time: now, Type: "mission.step_updated", JobID: req.JobID, Message: step.Title, Status: step.Status})
	return nil
}

func (s *Service) finishLocked(m *Mission, status string, now time.Time) error {
	if status != StatusSucceeded && status != StatusFailed && status != StatusCancelled {
		return errs.New(errs.InvalidArgument, "terminal mission status must be succeeded, failed, or cancelled")
	}
	if status == StatusSucceeded {
		verified := false
		for _, step := range m.Steps {
			if step.Status != StepPassed && step.Status != StepSkipped {
				return errs.New(errs.InvalidArgument, "cannot succeed while step %s is %s", step.ID, step.Status)
			}
			if step.Status == StepPassed {
				verified = true
			}
		}
		if !verified {
			return errs.New(errs.InvalidArgument, "cannot succeed without at least one passed step backed by a successful job")
		}
		for _, info := range s.mgr.ListJobs(m.Owner, "all") {
			if missionHasSession(m, info.SessionID) && !info.Status.Terminal() {
				return errs.New(errs.InvalidArgument, "cannot succeed while job %s is %s", info.JobID, info.Status)
			}
		}
	}
	m.Status = status
	completed := now
	m.CompletedAt = &completed
	m.Events = appendBounded(m.Events, Event{Time: now, Type: "mission.completed", Message: m.Summary, Status: status})
	return nil
}

// Get returns a mission scoped to owner. Empty owner is the authenticated
// operator view and can inspect all missions.
func (s *Service) Get(owner, id string) (*Mission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.getLocked(owner, id)
	if err != nil {
		return nil, err
	}
	return cloneMission(m), nil
}

// List returns newest missions first, scoped to owner when non-empty.
func (s *Service) List(owner, status string) []Mission {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Mission, 0, len(s.missions))
	for _, m := range s.missions {
		if owner != "" && m.Owner != owner {
			continue
		}
		if status != "" && m.Status != status {
			continue
		}
		copy := cloneMission(m)
		out = append(out, *copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// ListSummaries returns compact newest-first entries without timeline payloads.
func (s *Service) ListSummaries(owner, status string) []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.missions))
	for _, m := range s.missions {
		if owner != "" && m.Owner != owner {
			continue
		}
		if status != "" && m.Status != status {
			continue
		}
		passed := 0
		for _, step := range m.Steps {
			if step.Status == StepPassed || step.Status == StepSkipped {
				passed++
			}
		}
		out = append(out, Summary{
			ID: m.ID, Owner: m.Owner, Title: m.Title, Goal: m.Goal, Target: m.Target, Workspace: m.Workspace,
			SessionID: m.SessionID, Status: m.Status, StepsPassed: passed, StepsTotal: len(m.Steps),
			CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, CompletedAt: m.CompletedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// RecordEvent is a reliable bus sink. A persistence failure propagates to the
// engine so execution cannot silently outrun its promised evidence trail.
func (s *Service) RecordEvent(event bus.Event) error {
	if event.SessionID == "" || strings.HasPrefix(event.Type, "mission.") {
		return nil
	}
	s.mu.Lock()
	id := s.bySess[event.SessionID]
	m := s.missions[id]
	if m == nil || terminalStatus(m.Status) {
		s.mu.Unlock()
		return nil
	}
	projected, ok := s.projectEvent(event)
	if !ok {
		s.mu.Unlock()
		return nil
	}
	now := event.Time.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	projected.Time = now
	m.Events = appendBounded(m.Events, projected)
	m.UpdatedAt = now
	switch event.Type {
	case bus.EvJobStartRequested, bus.EvJobStarted:
		if m.StartedAt == nil {
			started := now
			m.StartedAt = &started
		}
		m.Status = StatusRunning
	case bus.EvConfirmRequested:
		m.Status = StatusNeedsAttention
	case bus.EvConfirmResolved:
		m.Status = StatusRunning
	case bus.EvSessionReset:
		m.Status = StatusNeedsAttention
	}
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}

func (s *Service) projectEvent(event bus.Event) (Event, bool) {
	allowed := map[string]bool{
		bus.EvJobStartRequested: true, bus.EvJobStarted: true, bus.EvJobFinished: true,
		bus.EvConfirmRequested: true, bus.EvConfirmResolved: true, bus.EvPolicyDenied: true,
		bus.EvSessionReset: true, bus.EvHumanInputAuthorized: true, "job.hold": true,
	}
	if !allowed[event.Type] {
		return Event{}, false
	}
	out := Event{Sequence: event.Sequence, Type: event.Type, JobID: event.JobID, Message: s.redacted(event.Message)}
	if status, ok := event.Data["status"].(engine.Status); ok {
		out.Status = string(status)
	} else if status, ok := event.Data["status"].(string); ok {
		out.Status = status
	}
	if value, ok := numericInt(event.Data["exit_code"]); ok {
		out.ExitCode = &value
	}
	if value, ok := event.Data["confirmation_id"].(string); ok {
		out.ConfirmationID = value
	}
	if value, ok := event.Data["approved"].(bool); ok {
		out.Approved = &value
	}
	if value, ok := event.Data["by"].(string); ok {
		out.By = s.redacted(value)
	}
	return out, true
}

func (s *Service) jobForMission(m *Mission, id string) (engine.Info, bool) {
	for _, info := range s.mgr.ListJobs(m.Owner, "all") {
		if info.JobID == id && missionHasSession(m, info.SessionID) {
			return info, true
		}
	}
	return engine.Info{}, false
}

func (s *Service) getLocked(owner, id string) (*Mission, error) {
	m := s.missions[id]
	if m == nil || (owner != "" && m.Owner != owner) {
		return nil, errs.New(errs.NotFound, "mission %q not found", id)
	}
	return m, nil
}

func (s *Service) emit(event bus.Event) error {
	if s.publish == nil {
		return nil
	}
	return s.publish(event)
}

func (s *Service) redacted(value string) string {
	if s.redact == nil {
		return value
	}
	return s.redact(value)
}

func (s *Service) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read mission store: %w", err)
	}
	var state diskState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode mission store: %w", err)
	}
	if state.Version != 1 {
		return fmt.Errorf("unsupported mission store version %d", state.Version)
	}
	if len(state.Missions) > maxMissions {
		return fmt.Errorf("mission store contains %d missions, exceeds %d", len(state.Missions), maxMissions)
	}
	for _, m := range state.Missions {
		if m == nil || m.ID == "" || len(m.Steps) > maxSteps || len(m.Events) > maxEvents {
			return fmt.Errorf("mission store contains invalid or oversized mission")
		}
		if len(m.SessionIDs) == 0 && m.SessionID != "" {
			m.SessionIDs = []string{m.SessionID}
		}
		s.missions[m.ID] = m
	}
	return nil
}

func (s *Service) persistLocked() error {
	missions := make([]*Mission, 0, len(s.missions))
	for _, mission := range s.missions {
		missions = append(missions, mission)
	}
	sort.Slice(missions, func(i, j int) bool { return missions[i].CreatedAt.Before(missions[j].CreatedAt) })
	data, err := json.MarshalIndent(diskState{Version: 1, Missions: missions}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".termada-missions-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Service) reindexLocked() {
	s.bySess = map[string]string{}
	for _, mission := range s.missions {
		if mission.SessionID != "" && !terminalStatus(mission.Status) {
			s.bySess[mission.SessionID] = mission.ID
		}
	}
}

func (s *Service) pruneLocked() {
	var oldest *Mission
	for _, mission := range s.missions {
		if !terminalStatus(mission.Status) {
			continue
		}
		if oldest == nil || mission.UpdatedAt.Before(oldest.UpdatedAt) {
			oldest = mission
		}
	}
	if oldest != nil {
		delete(s.missions, oldest.ID)
		delete(s.bySess, oldest.SessionID)
	}
}

func validateCreate(req CreateRequest) error {
	goal := strings.TrimSpace(req.Goal)
	if goal == "" || len(goal) > maxGoalBytes {
		return errs.New(errs.InvalidArgument, "goal is required and must be at most %d bytes", maxGoalBytes)
	}
	if len(strings.TrimSpace(req.Title)) > maxTitleBytes {
		return errs.New(errs.InvalidArgument, "title must be at most %d bytes", maxTitleBytes)
	}
	if len(req.Plan) == 0 || len(req.Plan) > maxSteps {
		return errs.New(errs.InvalidArgument, "plan must contain 1..%d steps", maxSteps)
	}
	for _, step := range req.Plan {
		trimmed := strings.TrimSpace(step)
		if trimmed == "" || len(trimmed) > maxStepTitleBytes {
			return errs.New(errs.InvalidArgument, "each plan step must be non-empty and at most %d bytes", maxStepTitleBytes)
		}
	}
	return nil
}

func deriveTitle(goal string) string {
	title := strings.TrimSpace(goal)
	if len(title) <= 72 {
		return title
	}
	return strings.TrimSpace(title[:69]) + "..."
}

func validStepStatus(status string) bool {
	return status == StepPending || status == StepRunning || status == StepPassed || status == StepFailed || status == StepSkipped
}

func terminalStatus(status string) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCancelled
}

func appendBounded(events []Event, event Event) []Event {
	events = append(events, event)
	if len(events) > maxEvents {
		events = append([]Event(nil), events[len(events)-maxEvents:]...)
	}
	return events
}

func cloneMission(m *Mission) *Mission {
	copy := *m
	copy.Steps = append([]Step(nil), m.Steps...)
	copy.Events = append([]Event(nil), m.Events...)
	copy.SessionIDs = append([]string(nil), m.SessionIDs...)
	return &copy
}

func missionHasSession(m *Mission, id string) bool {
	if id == "" {
		return false
	}
	if m.SessionID == id {
		return true
	}
	for _, sessionID := range m.SessionIDs {
		if sessionID == id {
			return true
		}
	}
	return false
}

func numericInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}
