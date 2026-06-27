// Package mcp implements a minimal Model Context Protocol server over a
// newline-delimited JSON-RPC 2.0 stdio transport (spec §22/§26). It deliberately
// has no external dependencies; migrating to the official MCP SDK is a later
// step.
package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"sync"

	"github.com/termada/termada/internal/errs"
)

const protocolVersion = "2024-11-05"

// serverInstructions is returned in the initialize result (MCP InitializeResult.
// instructions). Every connecting client sees it, even one without the termada
// skill — so it carries the load-bearing "default to termada, never the raw
// terminal" guidance and how the human-in-the-loop confirmation flow surfaces.
const serverInstructions = `termada gives you reliable persistent terminal sessions (local and remote-SSH).

USE THESE TOOLS FOR ALL SHELL WORK BY DEFAULT. Run every command through exec_run / exec_start instead of a built-in shell, and never shell out to a raw ` + "`ssh`" + ` client — go through termada so the human can watch, take over, and policy-gate the work.

- Sessions persist cwd and env. Omit ` + "`session`" + ` to use your per-agent default (state still persists); create a separate one with session_create, and for a remote server create session_create(target=<server-name>) and run in that session_id.
- Long-running jobs (dev servers, builds, watchers) run async: exec_start (or exec_run mode:"background") returns a job_id; read output with exec_poll(job_id, cursor), passing back next_cursor.
- Interactive prompts come back as status "awaiting_input" with the prompt — answer with exec_write(job_id, input) (secret:true for passwords).
- Dangerous commands come back as status "awaiting_confirmation" with a confirmation_id and need a HUMAN to approve. You CANNOT self-approve. Do not silently poll: tell the user in chat what the command will do and that it needs their approval (dashboard/CLI), then wait. denied_by_policy is final — don't try to bypass it.
- file_read / file_write act on the daemon host; for files on a remote server use exec_run in that server's session (cat / tee).

Call capabilities() once for a quickstart plus your allowed/denied policy summary and the registered servers.`

// Server serves MCP requests backed by a Backend (in-process engine or a daemon
// proxy).
type Server struct {
	backend Backend
	agentID string
	version string
	tools   map[string]toolDef
	order   []string
	logger  *log.Logger

	writeMu sync.Mutex
	enc     *json.Encoder
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// NewServer builds a server bound to the given backend and agent identity.
func NewServer(backend Backend, agentID, version string, logger *log.Logger) *Server {
	s := &Server{
		backend: backend,
		agentID: agentID,
		version: version,
		logger:  logger,
		tools:   map[string]toolDef{},
	}
	s.registerTools()
	return s
}

// ServeStdio reads JSON-RPC messages from r (one per line) and writes responses
// to w until EOF.
func (s *Server) ServeStdio(r io.Reader, w io.Writer) error {
	s.enc = json.NewEncoder(w)
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := readLine(br)
		if len(line) > 0 {
			s.handleLine(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// readLine reads a single newline-terminated message, tolerating arbitrarily
// long lines (bufio.Scanner's token cap is too small for big payloads).
func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadBytes('\n')
		buf = append(buf, chunk...)
		if err == nil {
			return trimNL(buf), nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return trimNL(buf), err
	}
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (s *Server) handleLine(line []byte) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.logf("parse error: %v", err)
		return
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	result, rerr := s.dispatch(req)
	if isNotification {
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	s.write(resp)
}

func (s *Server) dispatch(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		// Auto-detect the agent from clientInfo (e.g. "claude-code", "cursor") so
		// the dashboard attributes activity to a real name, and count the
		// connection (spec MA-1/MA-2).
		var p struct {
			ClientInfo struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ClientInfo.Name != "" && (s.agentID == "" || s.agentID == "default") {
			s.agentID = p.ClientInfo.Name
		}
		s.backend.RecordConnect(s.agentID)
		return map[string]any{
			"protocolVersion": protocolVersion,
			"serverInfo":      map[string]any{"name": "termada", "version": s.version},
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"instructions":    serverInstructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.toolList()}, nil
	case "tools/call":
		return s.callTool(req.Params)
	default:
		if len(req.Method) >= 14 && req.Method[:14] == "notifications/" {
			return nil, nil // notifications are fire-and-forget
		}
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (s *Server) callTool(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	def, ok := s.tools[p.Name]
	if !ok {
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}
	result, terr := def.Handler(p.Arguments)
	if terr != nil {
		return toolResult(errToValue(terr), true), nil
	}
	return toolResult(result, false), nil
}

// toolResult formats a value as an MCP tools/call result. Tool-level errors are
// returned as isError results (so the model sees them), not JSON-RPC errors. The
// content[].text is rendered human-legibly (prettyResult) for the chat
// transcript, while the exact object rides along in structuredContent for
// machine consumers — neither view drops an actionable field.
func toolResult(v any, isErr bool) map[string]any {
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": prettyResult(v, isErr)}},
		"structuredContent": v,
		"isError":           isErr,
	}
}

func (s *Server) write(resp rpcResponse) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.enc.Encode(resp); err != nil {
		s.logf("write error: %v", err)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// errToValue converts a structured engine error to the JSON the agent receives.
func errToValue(e *errs.Error) any {
	return map[string]any{"error": e}
}
