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
)

// Client talks to a running daemon's control-plane over a Unix socket. It
// implements mcp.Backend so the stdio shim can proxy to the daemon, and adds
// human-facing calls used by the CLI/TUI.
type Client struct {
	http *http.Client
	base string
}

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
	resp, err := c.http.Post(c.base+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return errs.New(errs.ServerUnreachable, "daemon unreachable: %v", err)
	}
	defer resp.Body.Close()
	return decodeResp(resp, out)
}

func (c *Client) get(path string, out any) error {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		return errs.New(errs.ServerUnreachable, "daemon unreachable: %v", err)
	}
	defer resp.Body.Close()
	return decodeResp(resp, out)
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

func (c *Client) Poll(jobID, cursor string) (*engine.PollResult, error) {
	var out engine.PollResult
	err := c.post("/api/exec/poll", execReq{JobID: jobID, Cursor: cursor}, &out)
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

func (c *Client) AuditTail(n int) ([]map[string]any, error) {
	var out struct {
		Records []map[string]any `json:"records"`
	}
	err := c.get(fmt.Sprintf("/api/audit?n=%d", n), &out)
	return out.Records, err
}
