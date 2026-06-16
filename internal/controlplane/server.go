// Package controlplane is the daemon's HTTP/JSON API (spec R4/§8). The daemon
// serves it over a Unix socket (for the stdio shim, CLI and TUI — local trust)
// and over loopback TCP with a token (for the browser dashboard).
package controlplane

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/termada/termada/internal/audit"
	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/fleet"
	"github.com/termada/termada/internal/plugin"
	"github.com/termada/termada/internal/vault"
)

// Server exposes the engine over HTTP/JSON.
type Server struct {
	mgr     *engine.Manager
	bus     *bus.Bus
	audit   *audit.Logger
	fleet   *fleet.Manager
	vault   *vault.Vault
	plugins *plugin.Manager
	version string
}

// New builds a control-plane server.
func New(mgr *engine.Manager, b *bus.Bus, a *audit.Logger, fl *fleet.Manager, v *vault.Vault, pl *plugin.Manager, version string) *Server {
	return &Server{mgr: mgr, bus: b, audit: a, fleet: fl, vault: v, plugins: pl, version: version}
}

// Mux returns the HTTP handler for the API and (later) the dashboard.
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session/create", s.hSessionCreate)
	mux.HandleFunc("/api/session/list", s.hSessionList)
	mux.HandleFunc("/api/session/close", s.hSessionClose)
	mux.HandleFunc("/api/exec/run", s.hExecRun)
	mux.HandleFunc("/api/exec/start", s.hExecStart)
	mux.HandleFunc("/api/exec/poll", s.hExecPoll)
	mux.HandleFunc("/api/exec/write", s.hExecWrite)
	mux.HandleFunc("/api/exec/signal", s.hExecSignal)
	mux.HandleFunc("/api/exec/kill", s.hExecKill)
	mux.HandleFunc("/api/exec/list", s.hExecList)
	mux.HandleFunc("/api/logs/tail", s.hLogsTail)
	mux.HandleFunc("/api/pending", s.hPending)
	mux.HandleFunc("/api/approve", s.hApprove)
	mux.HandleFunc("/api/deny", s.hDeny)
	mux.HandleFunc("/api/status", s.hStatus)
	mux.HandleFunc("/api/stop_all", s.hStopAll)
	mux.HandleFunc("/api/audit", s.hAudit)
	mux.HandleFunc("/api/events", s.hEvents)
	mux.HandleFunc("/api/file/read", s.hFileRead)
	mux.HandleFunc("/api/file/write", s.hFileWrite)
	mux.HandleFunc("/api/recipe/list", s.hRecipeList)
	mux.HandleFunc("/api/recipe/run", s.hRecipeRun)
	mux.HandleFunc("/api/servers", s.hServers)
	mux.HandleFunc("/api/fleet/run", s.hFleetRun)
	mux.HandleFunc("/api/vault/unlock", s.hVaultUnlock)
	mux.HandleFunc("/api/vault/status", s.hVaultStatus)
	mux.HandleFunc("/api/snapshot/create", s.hSnapshotCreate)
	mux.HandleFunc("/api/snapshot/restore", s.hSnapshotRestore)
	mux.HandleFunc("/api/snapshot/list", s.hSnapshotList)
	mux.HandleFunc("/api/plugin/tools", s.hPluginTools)
	mux.HandleFunc("/api/plugin/call", s.hPluginCall)
	mux.HandleFunc("/api/exec/hold", s.hExecHold)
	mux.HandleFunc("/api/exec/stream", s.hExecStream)
	mux.HandleFunc("/api/session/stream", s.hSessionStream)
	mux.HandleFunc("/api/session/write", s.hSessionWrite)
	mux.HandleFunc("/api/servers/add", s.hServerAdd)
	mux.HandleFunc("/api/servers/remove", s.hServerRemove)
	mux.HandleFunc("/api/servers/test", s.hServerTest)
	mux.HandleFunc("/api/agent/connect", s.hAgentConnect)
	mux.HandleFunc("/metrics", s.hMetrics)
	return mux
}

func (s *Server) hPluginTools(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeJSON(w, map[string]any{"tools": []any{}})
		return
	}
	writeJSON(w, map[string]any{"tools": s.plugins.Tools()})
}

func (s *Server) hPluginCall(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeErr(w, errs.New(errs.NotFound, "no plugins loaded"))
		return
	}
	var req struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	_ = decode(r, &req)
	res, err := s.plugins.Call(req.Name, req.Args)
	if err != nil {
		writeErr(w, errs.New(errs.Internal, "%v", err))
		return
	}
	writeJSON(w, res)
}

func (s *Server) hSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	res, err := s.mgr.SnapshotCreate(req.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	if err := s.mgr.SnapshotRestore(req.Name); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) hSnapshotList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"snapshots": s.mgr.SnapshotList()})
}

func (s *Server) hServers(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeJSON(w, map[string]any{"servers": []any{}})
		return
	}
	writeJSON(w, map[string]any{"servers": s.fleet.ServerList()})
}

// hServerAdd adds a server from the dashboard (human-only — never an MCP tool,
// per SEC-7). The credential is stored in the vault; only the reference is kept
// in the server inventory. Requires the vault unlocked.
func (s *Server) hServerAdd(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeErr(w, errs.New(errs.NotSupported, "fleet not available"))
		return
	}
	var req struct {
		Name   string   `json:"name"`
		Host   string   `json:"host"`
		Port   int      `json:"port"`
		User   string   `json:"user"`
		Auth   string   `json:"auth"`   // optional: reference an existing vault entry
		Secret string   `json:"secret"` // optional: SSH key or password to store now
		Tags   []string `json:"tags"`
	}
	_ = decode(r, &req)
	if req.Name == "" || req.Host == "" || req.User == "" {
		writeErr(w, errs.New(errs.InvalidArgument, "name, host and user are required"))
		return
	}
	auth := req.Auth
	if req.Secret != "" {
		if s.vault == nil || s.vault.Locked() {
			writeErr(w, errs.New(errs.VaultLocked, "unlock the vault first to store the credential"))
			return
		}
		if err := s.vault.Set(req.Name, req.Secret); err != nil {
			writeErr(w, errs.New(errs.Internal, "store credential: %v", err))
			return
		}
		s.mgr.Redactor().AddLiteral(req.Secret)
		auth = req.Name
	}
	if auth == "" {
		writeErr(w, errs.New(errs.InvalidArgument, "provide a credential (secret) or an existing vault entry (auth)"))
		return
	}
	if err := s.fleet.AddServer(fleet.Server{Name: req.Name, Host: req.Host, Port: req.Port, User: req.User, Auth: auth, Tags: req.Tags}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) hServerRemove(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeErr(w, errs.New(errs.NotSupported, "fleet not available"))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = decode(r, &req)
	if err := s.fleet.RemoveServer(req.Name); err != nil {
		writeErr(w, err)
		return
	}
	if s.vault != nil && !s.vault.Locked() {
		_ = s.vault.Delete(req.Name)
	}
	writeJSON(w, map[string]any{"ok": true})
}

// hServerTest checks connectivity by running `true` over SSH and reporting the
// per-server status (ok / unreachable / timeout / denied / conn_lost).
func (s *Server) hServerTest(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeErr(w, errs.New(errs.NotSupported, "fleet not available"))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	_ = decode(r, &req)
	res, err := s.fleet.Run([]string{"true"}, []string{req.Name}, 1)
	if err != nil {
		writeErr(w, err)
		return
	}
	status, detail := "unknown", ""
	if len(res.Results) > 0 {
		status = res.Results[0].Status
		detail = res.Results[0].Error
	}
	writeJSON(w, map[string]any{"status": status, "error": detail})
}

func (s *Server) hFleetRun(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeErr(w, errs.New(errs.NotSupported, "no servers configured"))
		return
	}
	var req struct {
		Command     []string `json:"command"`
		Servers     []string `json:"servers"`
		Tags        []string `json:"tags"`
		Parallelism int      `json:"parallelism"`
	}
	_ = decode(r, &req)
	selector := append(append([]string{}, req.Servers...), req.Tags...)
	res, err := s.fleet.Run(req.Command, selector, req.Parallelism)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hVaultUnlock(w http.ResponseWriter, r *http.Request) {
	if s.vault == nil {
		writeErr(w, errs.New(errs.NotSupported, "no vault configured"))
		return
	}
	var req struct {
		Passphrase string `json:"passphrase"`
	}
	_ = decode(r, &req)
	// First use: if no vault file exists yet, create it with this passphrase;
	// otherwise unlock the existing one. Lets a non-technical user set up
	// credentials entirely from the dashboard.
	var verr error
	if !s.vault.Exists() {
		verr = s.vault.Init(req.Passphrase)
	} else {
		verr = s.vault.Unlock(req.Passphrase)
	}
	if verr != nil {
		writeErr(w, errs.New(errs.VaultLocked, "%v", verr))
		return
	}
	// Register secrets with the redactor so they can never echo back to agents.
	for _, val := range s.vault.Values() {
		s.mgr.Redactor().AddLiteral(val)
	}
	writeJSON(w, map[string]any{"ok": true, "secrets": len(s.vault.List())})
}

func (s *Server) hVaultStatus(w http.ResponseWriter, r *http.Request) {
	locked := true
	exists := false
	if s.vault != nil {
		locked = s.vault.Locked()
		exists = s.vault.Exists()
	}
	writeJSON(w, map[string]any{"exists": exists, "locked": locked})
}

func (s *Server) hFileRead(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	res, err := s.mgr.FileRead(req.Path, req.MaxBytes)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hFileWrite(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	res, err := s.mgr.FileWrite(req.Path, req.Content, req.FileMode)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hRecipeList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"recipes": s.mgr.RecipeList()})
}

func (s *Server) hRecipeRun(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	res, err := s.mgr.RunRecipe(req.Owner, req.Session, req.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	e, ok := err.(*errs.Error)
	if !ok {
		e = errs.New(errs.Internal, "%v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": e})
}

// ownerFor resolves the acting agent: a configured agent token (header) wins
// over the self-asserted owner, making identity non-spoofable when tokens are
// configured (MA-2). Falls back to the asserted owner in local/dev mode.
func (s *Server) ownerFor(r *http.Request, asserted string) string {
	return s.mgr.ResolveAgent(r.Header.Get("X-Termada-Agent-Token"), asserted)
}

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	return json.NewDecoder(r.Body).Decode(v)
}

type execReq struct {
	Owner         string   `json:"owner"`
	Session       string   `json:"session"`
	Command       []string `json:"command"`
	Mode          string   `json:"mode"`
	TimeoutMS     int      `json:"timeout_ms"`
	JobID         string   `json:"job_id"`
	Cursor        string   `json:"cursor"`
	Input         string   `json:"input"`
	AppendNewline *bool    `json:"append_newline"`
	Secret        bool     `json:"secret"`
	Signal        string   `json:"signal"`
	Target        string   `json:"target"`
	SessionID     string   `json:"session_id"`
	ConfirmID     string   `json:"confirmation_id"`
	By            string   `json:"by"`
	Path          string   `json:"path"`
	MaxBytes      int      `json:"max_bytes"`
	Content       string   `json:"content"`
	FileMode      string   `json:"file_mode"`
	Name          string   `json:"name"`
	Human         bool     `json:"human"`
	HoldInput     *bool    `json:"hold_input"`
	HoldOutput    *bool    `json:"hold_output"`
}

func (s *Server) hSessionCreate(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	sess, err := s.mgr.CreateSession(s.ownerFor(r, req.Owner), req.Target, req.Mode)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, engine.SessionInfo{SessionID: sess.ID, Target: sess.Target, Mode: sess.Mode, Owner: sess.Owner})
}

func (s *Server) hSessionList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"sessions": s.mgr.ListSessions()})
}

func (s *Server) hSessionClose(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	if err := s.mgr.CloseSession(req.SessionID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) hExecRun(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	res, err := s.mgr.Run(s.ownerFor(r, req.Owner), req.Session, req.Command, req.Mode, req.TimeoutMS)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hExecStart(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	job, err := s.mgr.Start(s.ownerFor(r, req.Owner), req.Session, req.Command, req.Mode)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, job.Snapshot())
}

func (s *Server) hExecPoll(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	res, err := s.mgr.Poll(req.JobID, req.Cursor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hExecWrite(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	appendNL := true
	if req.AppendNewline != nil {
		appendNL = *req.AppendNewline
	}
	if err := s.mgr.Write(req.JobID, req.Input, appendNL, req.Secret, req.Human); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// hSessionStream streams a session's continuous terminal output (the whole
// shell, across jobs) as SSE — the live "session terminal" in the dashboard.
func (s *Server) hSessionStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	cursor := r.URL.Query().Get("cursor")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		res, err := s.mgr.SessionTail(sessionID, cursor)
		if err != nil {
			send(map[string]any{"error": err.Error()})
			return
		}
		if res.Chunk != "" {
			send(map[string]any{"chunk": res.Chunk})
		}
		cursor = res.NextCursor
		if res.Closed {
			send(map[string]any{"done": true})
			return
		}
		time.Sleep(120 * time.Millisecond)
	}
}

func (s *Server) hSessionWrite(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	appendNL := true
	if req.AppendNewline != nil {
		appendNL = *req.AppendNewline
	}
	if err := s.mgr.SessionWriteInput(req.SessionID, req.Input, appendNL, req.Human); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) hExecHold(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	if err := s.mgr.Hold(req.JobID, req.HoldInput, req.HoldOutput); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// hExecStream streams a job's (redacted) output as Server-Sent Events for the
// live terminal in the dashboard. It uses human=true so it always sees output,
// even when the agent's output is held.
func (s *Server) hExecStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	jobID := r.URL.Query().Get("job_id")
	cursor := r.URL.Query().Get("cursor")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		res, err := s.mgr.PollFor(jobID, cursor, true)
		if err != nil {
			send(map[string]any{"error": err.Error()})
			return
		}
		if res.StdoutChunk != "" {
			send(map[string]any{"chunk": res.StdoutChunk, "status": res.Status})
		}
		cursor = res.NextCursor
		if res.Status.Terminal() {
			send(map[string]any{"status": res.Status, "done": true})
			return
		}
		time.Sleep(120 * time.Millisecond)
	}
}

func (s *Server) hExecSignal(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	if err := s.mgr.Signal(req.JobID, req.Signal); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) hExecKill(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	if err := s.mgr.Kill(req.JobID); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) hExecList(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	writeJSON(w, map[string]any{"jobs": s.mgr.ListJobs(filter)})
}

func (s *Server) hLogsTail(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	res, err := s.mgr.Tail(req.JobID, req.Cursor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hPending(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"pending": s.mgr.ListPending()})
}

func (s *Server) hApprove(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	by := req.By
	if by == "" {
		by = "dashboard"
	}
	if err := s.mgr.Approve(req.ConfirmID, by); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) hDeny(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	by := req.By
	if by == "" {
		by = "dashboard"
	}
	if err := s.mgr.Deny(req.ConfirmID, by); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// hMetrics exposes Prometheus metrics (spec §8.6).
func (s *Server) hMetrics(w http.ResponseWriter, r *http.Request) {
	jobs := s.mgr.ListJobs("all")
	active := 0
	for _, j := range jobs {
		if !j.Status.Terminal() {
			active++
		}
	}
	agents := s.mgr.Agents()
	conns := 0
	for _, a := range agents {
		conns += a.Connections
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	out := func(name, help, typ string, v int) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, typ, name, v)
	}
	out("termada_jobs_total", "Jobs known to the registry.", "gauge", len(jobs))
	out("termada_jobs_active", "Jobs currently running/awaiting.", "gauge", active)
	out("termada_sessions", "Open sessions.", "gauge", len(s.mgr.ListSessions()))
	out("termada_pending_approvals", "Commands awaiting human approval.", "gauge", len(s.mgr.ListPending()))
	out("termada_agents", "Distinct agents seen.", "gauge", len(agents))
	out("termada_agent_connections_total", "Total agent connections.", "counter", conns)
}

func (s *Server) hAgentConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent string `json:"agent"`
	}
	_ = decode(r, &req)
	s.mgr.RecordConnect(s.ownerFor(r, req.Agent))
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) hStatus(w http.ResponseWriter, r *http.Request) {
	servers := []fleet.ServerInfo{}
	if s.fleet != nil {
		servers = s.fleet.ServerList()
	}
	writeJSON(w, map[string]any{
		"version":  s.version,
		"sessions": s.mgr.ListSessions(),
		"jobs":     s.mgr.ListJobs("active"),
		"pending":  s.mgr.ListPending(),
		"servers":  servers,
		"agents":   s.mgr.Agents(),
	})
}

func (s *Server) hStopAll(w http.ResponseWriter, r *http.Request) {
	n := 0
	for _, j := range s.mgr.ListJobs("active") {
		if !j.Status.Terminal() {
			if s.mgr.Kill(j.JobID) == nil {
				n++
			}
		}
	}
	writeJSON(w, map[string]any{"killed": n})
}

func (s *Server) hAudit(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v := r.URL.Query().Get("n"); v != "" {
		if x, err := strconv.Atoi(v); err == nil {
			n = x
		}
	}
	if s.audit == nil {
		writeJSON(w, map[string]any{"records": []any{}})
		return
	}
	recs, err := s.audit.Tail(n)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"records": recs})
}

// hEvents streams bus events as Server-Sent Events.
func (s *Server) hEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok || s.bus == nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.bus.Subscribe(256)
	defer cancel()

	// Replay recent events so a fresh dashboard has context.
	for _, e := range s.bus.Recent(50) {
		writeSSE(w, e)
	}
	flusher.Flush()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, e)
			flusher.Flush()
		case <-ping.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, e bus.Event) {
	b, _ := json.Marshal(e)
	var sb strings.Builder
	sb.WriteString("data: ")
	sb.Write(b)
	sb.WriteString("\n\n")
	_, _ = w.Write([]byte(sb.String()))
}
