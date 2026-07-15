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
	Poll(owner, jobID, cursor string, waitMS int) (*engine.PollResult, error)
	Write(owner, jobID, input string, appendNewline, secret bool) error
	Signal(owner, jobID, signal string) error
	Kill(owner, jobID string) error
	ListJobs(owner, filter string) []engine.Info
	CreateSession(owner, target, mode, workspace string) (engine.SessionInfo, error)
	ListSessions(owner string) []engine.SessionInfo
	CloseSession(owner, id string) error
	Tail(owner, jobID, cursor string) (*engine.TailResult, error)
	// FileRead/FileWrite are session-aware: with an empty or local session they
	// act on the daemon host; with a remote session they refuse loudly (instead
	// of silently touching the local FS while the agent believes it is remote).
	FileRead(owner, session, path string, maxBytes int) (*engine.FileReadResult, error)
	FileWrite(owner, session, path, content, mode string) (*engine.FileWriteResult, error)
	RecipeList() []engine.RecipeInfo
	RecipeRun(owner, session, name string) (*engine.RecipeRunResult, error)
	ServerList() []fleet.ServerInfo
	FleetRun(owner string, command []string, selector []string, parallelism int) (*fleet.RunResult, error)
	PortForward(owner, server, remoteHost string, remotePort int, localBind string) (*engine.ForwardInfo, error)
	PortForwardList(owner string) []engine.ForwardInfo
	PortForwardClose(owner, id string) error
	PluginTools() []plugin.ToolSpec
	PluginCall(owner, name string, args map[string]any) (any, error)
	RecordConnect(agent string)
	// RemoteAvailable reports whether remote (SSH) sessions, fleet and plugins
	// are reachable — true only when backed by a running daemon. The in-process
	// fallback returns false so the agent can tell it is in degraded mode
	// instead of silently landing on a local shell.
	RemoteAvailable() bool
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

func (b *LocalBackend) Poll(owner, jobID, cursor string, waitMS int) (*engine.PollResult, error) {
	return b.m.PollWait(owner, jobID, cursor, waitMS)
}

func (b *LocalBackend) Write(owner, jobID, input string, appendNewline, secret bool) error {
	// Agent-originated input (human=false): rejected while a human holds input.
	return b.m.Write(owner, jobID, input, appendNewline, secret, false)
}

func (b *LocalBackend) Signal(owner, jobID, signal string) error {
	return b.m.Signal(owner, jobID, signal)
}
func (b *LocalBackend) Kill(owner, jobID string) error { return b.m.Kill(owner, jobID) }
func (b *LocalBackend) ListJobs(owner, filter string) []engine.Info {
	return b.m.ListJobs(owner, filter)
}

func (b *LocalBackend) CreateSession(owner, target, mode, workspace string) (engine.SessionInfo, error) {
	// Remote sessions need the daemon (server inventory + vault). Reject them
	// loudly here instead of letting the agent fall through to a silent local
	// default session and run remote-intended commands on this host.
	if target != "" && target != "local" {
		return engine.SessionInfo{}, errs.New(errs.NotSupported,
			"remote session to %q requires the termada daemon (none running); start it with `termada serve`. In-process mode supports target=local only", target)
	}
	sess, err := b.m.CreateSessionWithWorkspace(owner, target, mode, workspace)
	if err != nil {
		return engine.SessionInfo{}, err
	}
	return engine.SessionInfo{
		SessionID: sess.ID, Target: sess.Target, Mode: sess.Mode, Owner: sess.Owner, Workspace: sess.Workspace,
		CreatedUnix: sess.CreatedAt.Unix(), CreatedUnixMS: sess.CreatedAt.UnixMilli(),
	}, nil
}

func (b *LocalBackend) ListSessions(owner string) []engine.SessionInfo {
	return b.m.ListSessionsFor(owner)
}
func (b *LocalBackend) CloseSession(owner, id string) error {
	return b.m.CloseSession(owner, id)
}
func (b *LocalBackend) Tail(owner, jobID, cursor string) (*engine.TailResult, error) {
	// Agent-originated (human=false): honors an output hold like exec_poll.
	return b.m.Tail(owner, jobID, cursor, false)
}

func (b *LocalBackend) FileRead(owner, session, path string, maxBytes int) (*engine.FileReadResult, error) {
	return b.m.FileReadFor(owner, session, path, maxBytes)
}

func (b *LocalBackend) FileWrite(owner, session, path, content, mode string) (*engine.FileWriteResult, error) {
	return b.m.FileWriteFor(owner, session, path, content, mode)
}

func (b *LocalBackend) RecipeList() []engine.RecipeInfo { return b.m.RecipeList() }

func (b *LocalBackend) RecipeRun(owner, session, name string) (*engine.RecipeRunResult, error) {
	return b.m.RunRecipe(owner, session, name)
}

// ServerList / FleetRun are daemon-only (they need the vault and server
// inventory); the in-process fallback has neither.
func (b *LocalBackend) RecordConnect(agent string) { b.m.RecordConnect(agent) }

func (b *LocalBackend) ServerList() []fleet.ServerInfo { return nil }

// RemoteAvailable is false in-process: no daemon, so no server inventory, vault
// or SSH. The agent should treat target=local as the only option here.
func (b *LocalBackend) RemoteAvailable() bool { return false }

func (b *LocalBackend) FleetRun(owner string, command []string, selector []string, parallelism int) (*fleet.RunResult, error) {
	return nil, errs.New(errs.NotSupported, "fleet requires a running daemon (run: termada serve)")
}

// Port forwarding needs the daemon (server inventory + SSH runner); the
// in-process fallback returns NotSupported via the engine.
func (b *LocalBackend) PortForward(owner, server, remoteHost string, remotePort int, localBind string) (*engine.ForwardInfo, error) {
	return b.m.PortForward(owner, server, remoteHost, remotePort, localBind)
}
func (b *LocalBackend) PortForwardList(owner string) []engine.ForwardInfo {
	return b.m.PortForwardList(owner)
}
func (b *LocalBackend) PortForwardClose(owner, id string) error {
	return b.m.PortForwardClose(owner, id)
}

// Plugins are daemon-only (loaded from the plugins dir); the in-process fallback
// has none.
func (b *LocalBackend) PluginTools() []plugin.ToolSpec { return nil }

func (b *LocalBackend) PluginCall(owner, name string, args map[string]any) (any, error) {
	return nil, errs.New(errs.NotSupported, "plugins require a running daemon")
}
