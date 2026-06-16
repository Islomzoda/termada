package mcp

import (
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/fleet"
	"github.com/termada/termada/internal/plugin"
)

// Backend is the set of operations the MCP tools need. It is satisfied both by
// an in-process engine (LocalBackend) and by a client that proxies to a running
// daemon over the control-plane, which is what lets `serve --stdio` be a thin
// shim (spec R4).
type Backend interface {
	Run(owner, session string, command []string, mode string, timeoutMS int) (*engine.RunResult, error)
	Start(owner, session string, command []string, mode string) (engine.Info, error)
	Poll(jobID, cursor string) (*engine.PollResult, error)
	Write(jobID, input string, appendNewline, secret bool) error
	Signal(jobID, signal string) error
	Kill(jobID string) error
	ListJobs(filter string) []engine.Info
	CreateSession(owner, target, mode string) (engine.SessionInfo, error)
	ListSessions() []engine.SessionInfo
	CloseSession(id string) error
	Tail(jobID, cursor string) (*engine.TailResult, error)
	FileRead(path string, maxBytes int) (*engine.FileReadResult, error)
	FileWrite(path, content, mode string) (*engine.FileWriteResult, error)
	RecipeList() []engine.RecipeInfo
	RecipeRun(owner, session, name string) (*engine.RecipeRunResult, error)
	ServerList() []fleet.ServerInfo
	FleetRun(command []string, selector []string, parallelism int) (*fleet.RunResult, error)
	PluginTools() []plugin.ToolSpec
	PluginCall(name string, args map[string]any) (any, error)
	RecordConnect(agent string)
}

// LocalBackend adapts an in-process *engine.Manager to the Backend interface.
type LocalBackend struct{ m *engine.Manager }

// NewLocalBackend wraps a manager.
func NewLocalBackend(m *engine.Manager) *LocalBackend { return &LocalBackend{m: m} }

func (b *LocalBackend) Run(owner, session string, command []string, mode string, timeoutMS int) (*engine.RunResult, error) {
	return b.m.Run(owner, session, command, mode, timeoutMS)
}

func (b *LocalBackend) Start(owner, session string, command []string, mode string) (engine.Info, error) {
	job, err := b.m.Start(owner, session, command, mode)
	if err != nil {
		return engine.Info{}, err
	}
	return job.Snapshot(), nil
}

func (b *LocalBackend) Poll(jobID, cursor string) (*engine.PollResult, error) {
	return b.m.Poll(jobID, cursor)
}

func (b *LocalBackend) Write(jobID, input string, appendNewline, secret bool) error {
	// Agent-originated input (human=false): rejected while a human holds input.
	return b.m.Write(jobID, input, appendNewline, secret, false)
}

func (b *LocalBackend) Signal(jobID, signal string) error { return b.m.Signal(jobID, signal) }
func (b *LocalBackend) Kill(jobID string) error           { return b.m.Kill(jobID) }
func (b *LocalBackend) ListJobs(filter string) []engine.Info {
	return b.m.ListJobs(filter)
}

func (b *LocalBackend) CreateSession(owner, target, mode string) (engine.SessionInfo, error) {
	sess, err := b.m.CreateSession(owner, target, mode)
	if err != nil {
		return engine.SessionInfo{}, err
	}
	return engine.SessionInfo{SessionID: sess.ID, Target: sess.Target, Mode: sess.Mode, Owner: sess.Owner}, nil
}

func (b *LocalBackend) ListSessions() []engine.SessionInfo { return b.m.ListSessions() }
func (b *LocalBackend) CloseSession(id string) error       { return b.m.CloseSession(id) }
func (b *LocalBackend) Tail(jobID, cursor string) (*engine.TailResult, error) {
	return b.m.Tail(jobID, cursor)
}

func (b *LocalBackend) FileRead(path string, maxBytes int) (*engine.FileReadResult, error) {
	return b.m.FileRead(path, maxBytes)
}

func (b *LocalBackend) FileWrite(path, content, mode string) (*engine.FileWriteResult, error) {
	return b.m.FileWrite(path, content, mode)
}

func (b *LocalBackend) RecipeList() []engine.RecipeInfo { return b.m.RecipeList() }

func (b *LocalBackend) RecipeRun(owner, session, name string) (*engine.RecipeRunResult, error) {
	return b.m.RunRecipe(owner, session, name)
}

// ServerList / FleetRun are daemon-only (they need the vault and server
// inventory); the in-process fallback has neither.
func (b *LocalBackend) RecordConnect(agent string) { b.m.RecordConnect(agent) }

func (b *LocalBackend) ServerList() []fleet.ServerInfo { return nil }

func (b *LocalBackend) FleetRun(command []string, selector []string, parallelism int) (*fleet.RunResult, error) {
	return nil, errs.New(errs.NotSupported, "fleet requires a running daemon (run: termada serve)")
}

// Plugins are daemon-only (loaded from the plugins dir); the in-process fallback
// has none.
func (b *LocalBackend) PluginTools() []plugin.ToolSpec { return nil }

func (b *LocalBackend) PluginCall(name string, args map[string]any) (any, error) {
	return nil, errs.New(errs.NotSupported, "plugins require a running daemon")
}
