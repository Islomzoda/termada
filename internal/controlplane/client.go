package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/fleet"
	"github.com/termada/termada/internal/plugin"
)

// Client talks to a running daemon's control-plane over a Unix socket. It
// implements mcp.Backend so the stdio shim can proxy to the daemon, and adds
// human-facing calls used by the CLI/TUI.
type Client struct {
	http     *http.Client
	base     string
	token    string // optional per-agent identity token (X-Termada-Agent-Token)
	cliToken string // optional human-CLI auth token (X-Termada-CLI-Token) for approve/deny/stop over the UDS
}

// SetToken sets the per-agent identity token sent with every request.
func (c *Client) SetToken(t string) { c.token = t }

// SetCLIToken sets the human-CLI auth token sent with every request. The daemon
// requires it on the approval routes (approve/deny/stop_all) over the local
// socket so an agent cannot self-approve; only the human CLI (which can read the
// 0600 cli.token file) presents it.
func (c *Client) SetCLIToken(t string) { c.cliToken = t }

// NewUnixClient returns a client bound to the daemon's Unix socket.
func NewUnixClient(socketPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{http: &http.Client{Transport: tr}, base: "http://unix"}
}

func (c *Client) post(path string, req, out any) error {
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	c.auth(r)
	resp, err := c.http.Do(r)
	if err != nil {
		return errs.New(errs.ServerUnreachable, "daemon unreachable: %v", err)
	}
	defer resp.Body.Close()
	return decodeResp(resp, out)
}

func (c *Client) get(path string, out any) error {
	r, _ := http.NewRequest(http.MethodGet, c.base+path, nil)
	c.auth(r)
	resp, err := c.http.Do(r)
	if err != nil {
		return errs.New(errs.ServerUnreachable, "daemon unreachable: %v", err)
	}
	defer resp.Body.Close()
	return decodeResp(resp, out)
}

func (c *Client) auth(r *http.Request) {
	if c.token != "" {
		r.Header.Set("X-Termada-Agent-Token", c.token)
	}
	// Harmless on every request; the daemon only checks it on the approval routes
	// over the UDS. The agent shim never sets it, so it cannot self-approve.
	if c.cliToken != "" {
		r.Header.Set("X-Termada-CLI-Token", c.cliToken)
	}
}

func decodeResp(resp *http.Response, out any) error {
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error *errs.Error `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error != nil {
			return e.Error
		}
		return errs.New(errs.Internal, "daemon returned HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Ping checks whether the daemon is reachable.
func (c *Client) Ping() error {
	return c.get("/api/status", nil)
}

// ---- mcp.Backend implementation ----

func (c *Client) Run(owner, session string, command []string, mode string, timeoutMS int) (*engine.RunResult, error) {
	var out engine.RunResult
	err := c.post("/api/exec/run", execReq{Owner: owner, Session: session, Command: command, Mode: mode, TimeoutMS: timeoutMS}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Start(owner, session string, command []string, mode string) (engine.Info, error) {
	var out engine.Info
	err := c.post("/api/exec/start", execReq{Owner: owner, Session: session, Command: command, Mode: mode}, &out)
	return out, err
}

func (c *Client) Poll(jobID, cursor string, waitMS int) (*engine.PollResult, error) {
	var out engine.PollResult
	err := c.post("/api/exec/poll", execReq{JobID: jobID, Cursor: cursor, WaitMS: waitMS}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Write(jobID, input string, appendNewline, secret bool) error {
	return c.post("/api/exec/write", execReq{JobID: jobID, Input: input, AppendNewline: &appendNewline, Secret: secret}, nil)
}

func (c *Client) Signal(jobID, signal string) error {
	return c.post("/api/exec/signal", execReq{JobID: jobID, Signal: signal}, nil)
}

func (c *Client) Kill(jobID string) error {
	return c.post("/api/exec/kill", execReq{JobID: jobID}, nil)
}

func (c *Client) ListJobs(filter string) []engine.Info {
	var out struct {
		Jobs []engine.Info `json:"jobs"`
	}
	_ = c.get("/api/exec/list?filter="+url.QueryEscape(filter), &out)
	return out.Jobs
}

func (c *Client) CreateSession(owner, target, mode string) (engine.SessionInfo, error) {
	var out engine.SessionInfo
	err := c.post("/api/session/create", execReq{Owner: owner, Target: target, Mode: mode}, &out)
	return out, err
}

func (c *Client) ListSessions() []engine.SessionInfo {
	var out struct {
		Sessions []engine.SessionInfo `json:"sessions"`
	}
	_ = c.get("/api/session/list", &out)
	return out.Sessions
}

func (c *Client) CloseSession(id string) error {
	return c.post("/api/session/close", execReq{SessionID: id}, nil)
}

func (c *Client) Tail(jobID, cursor string) (*engine.TailResult, error) {
	var out engine.TailResult
	err := c.post("/api/logs/tail", execReq{JobID: jobID, Cursor: cursor}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) FileRead(session, path string, maxBytes int) (*engine.FileReadResult, error) {
	var out engine.FileReadResult
	err := c.post("/api/file/read", execReq{Session: session, Path: path, MaxBytes: maxBytes}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) FileWrite(session, path, content, mode string) (*engine.FileWriteResult, error) {
	var out engine.FileWriteResult
	err := c.post("/api/file/write", execReq{Session: session, Path: path, Content: content, FileMode: mode}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) RecipeList() []engine.RecipeInfo {
	var out struct {
		Recipes []engine.RecipeInfo `json:"recipes"`
	}
	_ = c.get("/api/recipe/list", &out)
	return out.Recipes
}

func (c *Client) RecipeRun(owner, session, name string) (*engine.RecipeRunResult, error) {
	var out engine.RecipeRunResult
	err := c.post("/api/recipe/run", execReq{Owner: owner, Session: session, Name: name}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ServerList() []fleet.ServerInfo {
	var out struct {
		Servers []fleet.ServerInfo `json:"servers"`
	}
	_ = c.get("/api/servers", &out)
	return out.Servers
}

// RemoteAvailable is true for the daemon-backed client: remote SSH sessions,
// fleet and the vault are reachable (subject to server config and an unlocked
// vault). Contrast with the in-process LocalBackend.
func (c *Client) RemoteAvailable() bool { return true }

func (c *Client) FleetRun(command []string, selector []string, parallelism int) (*fleet.RunResult, error) {
	body := map[string]any{"command": command, "servers": selector, "parallelism": parallelism}
	var out fleet.RunResult
	if err := c.post("/api/fleet/run", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) PortForward(server, remoteHost string, remotePort int, localBind string) (*engine.ForwardInfo, error) {
	body := map[string]any{"server": server, "remote_host": remoteHost, "remote_port": remotePort, "local_bind": localBind}
	var out engine.ForwardInfo
	if err := c.post("/api/forward/start", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) PortForwardList() []engine.ForwardInfo {
	var out struct {
		Forwards []engine.ForwardInfo `json:"forwards"`
	}
	_ = c.post("/api/forward/list", map[string]any{}, &out)
	return out.Forwards
}

func (c *Client) PortForwardClose(id string) error {
	return c.post("/api/forward/close", map[string]any{"id": id}, nil)
}

func (c *Client) PluginTools() []plugin.ToolSpec {
	var out struct {
		Tools []plugin.ToolSpec `json:"tools"`
	}
	_ = c.get("/api/plugin/tools", &out)
	return out.Tools
}

func (c *Client) PluginCall(owner, name string, args map[string]any) (any, error) {
	var out any
	err := c.post("/api/plugin/call", map[string]any{"owner": owner, "name": name, "args": args}, &out)
	return out, err
}

// RecordConnect notifies the daemon that an agent connected (best-effort).
func (c *Client) RecordConnect(agent string) {
	_ = c.post("/api/agent/connect", map[string]string{"agent": agent}, nil)
}

// Unlock sends the vault passphrase to the daemon.
func (c *Client) Unlock(passphrase string) (int, error) {
	var out struct {
		Secrets int `json:"secrets"`
	}
	err := c.post("/api/vault/unlock", map[string]string{"passphrase": passphrase}, &out)
	return out.Secrets, err
}

// ---- human/CLI calls ----

// Status is the daemon overview.
type Status struct {
	Version  string               `json:"version"`
	Sessions []engine.SessionInfo `json:"sessions"`
	Jobs     []engine.Info        `json:"jobs"`
	Pending  []engine.PendingInfo `json:"pending"`
}

func (c *Client) Status() (*Status, error) {
	var out Status
	if err := c.get("/api/status", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Pending() ([]engine.PendingInfo, error) {
	var out struct {
		Pending []engine.PendingInfo `json:"pending"`
	}
	err := c.get("/api/pending", &out)
	return out.Pending, err
}

func (c *Client) Approve(confirmID, by string) error {
	return c.post("/api/approve", execReq{ConfirmID: confirmID, By: by}, nil)
}

func (c *Client) Deny(confirmID, by string) error {
	return c.post("/api/deny", execReq{ConfirmID: confirmID, By: by}, nil)
}

func (c *Client) StopAll() (int, error) {
	var out struct {
		Killed int `json:"killed"`
	}
	err := c.post("/api/stop_all", struct{}{}, &out)
	return out.Killed, err
}

func (c *Client) SnapshotCreate(path string) (*engine.SnapshotInfo, error) {
	var out engine.SnapshotInfo
	if err := c.post("/api/snapshot/create", execReq{Path: path}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) SnapshotRestore(id string) error {
	return c.post("/api/snapshot/restore", execReq{Name: id}, nil)
}

func (c *Client) SnapshotList() ([]engine.SnapshotInfo, error) {
	var out struct {
		Snapshots []engine.SnapshotInfo `json:"snapshots"`
	}
	err := c.get("/api/snapshot/list", &out)
	return out.Snapshots, err
}

func (c *Client) AuditTail(n int) ([]map[string]any, error) {
	var out struct {
		Records []map[string]any `json:"records"`
	}
	err := c.get(fmt.Sprintf("/api/audit?n=%d", n), &out)
	return out.Records, err
}
