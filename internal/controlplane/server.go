// Package controlplane is the daemon's HTTP/JSON API (spec R4/§8). The daemon
// serves it over a Unix socket (for the stdio shim, CLI and TUI — local trust)
// and over loopback TCP with a token (for the browser dashboard).
package controlplane

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/termada/termada/internal/audit"
	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/errs"
)

// Server exposes the engine over HTTP/JSON.
type Server struct {
	mgr     *engine.Manager
	bus     *bus.Bus
	audit   *audit.Logger
	version string
}

// New builds a control-plane server.
func New(mgr *engine.Manager, b *bus.Bus, a *audit.Logger, version string) *Server {
	return &Server{mgr: mgr, bus: b, audit: a, version: version}
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
	return mux
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
}

func (s *Server) hSessionCreate(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	sess, err := s.mgr.CreateSession(req.Owner, req.Target, req.Mode)
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
	res, err := s.mgr.Run(req.Owner, req.Session, req.Command, req.Mode, req.TimeoutMS)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) hExecStart(w http.ResponseWriter, r *http.Request) {
	var req execReq
	_ = decode(r, &req)
	job, err := s.mgr.Start(req.Owner, req.Session, req.Command, req.Mode)
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
	if err := s.mgr.Write(req.JobID, req.Input, appendNL, req.Secret); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
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

func (s *Server) hStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"version":  s.version,
		"sessions": s.mgr.ListSessions(),
		"jobs":     s.mgr.ListJobs("active"),
		"pending":  s.mgr.ListPending(),
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
