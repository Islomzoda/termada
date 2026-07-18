package controlplane

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/termada/termada/internal/audit"
	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/fleet"
	"github.com/termada/termada/internal/mission"
	"github.com/termada/termada/internal/plugin"
	"github.com/termada/termada/internal/policy"
	"github.com/termada/termada/internal/vault"
)

type okRunner struct{}

func (okRunner) Run(s fleet.Server, command []string) fleet.Result {
	return fleet.Result{Server: s.Name, Status: "ok"}
}

type countingRunner struct{ calls int }

func (r *countingRunner) Run(s fleet.Server, command []string) fleet.Result {
	r.calls++
	return fleet.Result{Server: s.Name, Status: "ok"}
}

type secretRunner struct{}

func (secretRunner) Run(s fleet.Server, command []string) fleet.Result {
	return fleet.Result{Server: s.Name, Status: "nonzero_exit", Stdout: "stdout fleet-secret", Stderr: "stderr fleet-secret", Error: "error fleet-secret"}
}

type testForwards struct {
	items map[string]engine.ForwardInfo
}

func (f *testForwards) Start(owner, server, remoteHost string, remotePort int, localBind string) (engine.ForwardInfo, error) {
	info := engine.ForwardInfo{ID: "fwd_test", Owner: owner, Server: server, RemoteHost: remoteHost, RemotePort: remotePort, LocalAddr: "127.0.0.1:1234"}
	f.items[info.ID] = info
	return info, nil
}

func (f *testForwards) List(owner string) []engine.ForwardInfo {
	out := []engine.ForwardInfo{}
	for _, info := range f.items {
		if owner == "" || info.Owner == owner {
			out = append(out, info)
		}
	}
	return out
}

func (f *testForwards) Close(owner, id string) error {
	info, ok := f.items[id]
	if !ok || (owner != "" && info.Owner != owner) {
		return os.ErrNotExist
	}
	delete(f.items, id)
	return nil
}

func newTestServer(t *testing.T) (*http.ServeMux, *engine.Manager) {
	t.Helper()
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	b := bus.New(100)
	m.SetBus(b)
	v := vault.New(filepath.Join(t.TempDir(), "v.age"))
	fl := fleet.New(nil, okRunner{}, 2)
	return New(m, b, nil, fl, v, nil, "test").Mux(), m
}

func newAuditedTestServer(t *testing.T) (*http.ServeMux, *engine.Manager, *audit.Logger, string) {
	t.Helper()
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	b := bus.New(100)
	m.SetBus(b)
	path := filepath.Join(t.TempDir(), "audit.log")
	logger, err := audit.Open(path, m.Redactor())
	if err != nil {
		t.Fatalf("open audit: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	cancel := b.SubscribeReliable(logger.FromBus)
	t.Cleanup(cancel)
	m.SetAuditHealth(logger.Healthy)
	return New(m, b, logger, nil, nil, nil, "test").Mux(), m, logger, path
}

func firstSSEEvent(t *testing.T, s *Server, path string) map[string]any {
	t.Helper()
	mux := s.Mux()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, WithOperatorPrincipal(r))
	}))
	defer ts.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE stream: %v", err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &event); err != nil {
			t.Fatalf("decode SSE event: %v", err)
		}
		return event
	}
}

func do(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func doOperator(t *testing.T, mux *http.ServeMux, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r = WithOperatorPrincipal(r)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func doOperatorSource(t *testing.T, mux *http.ServeMux, source, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r = WithOperatorPrincipalSource(r, source)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func doWithAgentToken(t *testing.T, mux *http.ServeMux, method, path, body, token string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("X-Termada-Agent-Token", token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestAPIExecRun(t *testing.T) {
	mux, _ := newTestServer(t)
	code, out := do(t, mux, "POST", "/api/exec/run", `{"owner":"t","command":["echo","api-hello"]}`)
	if code != 200 {
		t.Fatalf("code=%d body=%v", code, out)
	}
	// Output is captured regardless of whether the job has finalized yet.
	if s, _ := out["stdout"].(string); !strings.Contains(s, "api-hello") {
		t.Fatalf("stdout = %v", out["stdout"])
	}
	// Under heavy parallel test load the marker can land just after the run
	// window, leaving the job briefly "backgrounded"; poll it to terminal.
	status, _ := out["status"].(string)
	if status != "exited" {
		jid, _ := out["job_id"].(string)
		for i := 0; i < 100 && status != "exited"; i++ {
			time.Sleep(50 * time.Millisecond)
			_, p := do(t, mux, "POST", "/api/exec/poll", `{"owner":"t","job_id":"`+jid+`"}`)
			status, _ = p["status"].(string)
		}
	}
	if status != "exited" {
		t.Fatalf("did not reach exited: status=%v", status)
	}
}

func TestMissionAPITracksRealJobEvidenceAndOwnerScope(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	b := bus.New(100)
	m.SetBus(b)
	service, err := mission.New(filepath.Join(t.TempDir(), "missions.json"), m, b.Publish, m.Redactor().Redact)
	if err != nil {
		t.Fatal(err)
	}
	cancel := b.SubscribeReliable(service.RecordEvent)
	t.Cleanup(cancel)
	server := New(m, b, nil, nil, nil, nil, "test")
	server.SetMissions(service)
	mux := server.Mux()

	code, created := do(t, mux, http.MethodPost, "/api/mission/create", `{"owner":"codex","goal":"repair demo","plan":["diagnose","verify"]}`)
	if code != http.StatusOK {
		t.Fatalf("create code=%d body=%v", code, created)
	}
	id, _ := created["id"].(string)
	sessionID, _ := created["session_id"].(string)
	if id == "" || sessionID == "" {
		t.Fatalf("missing mission/session id: %v", created)
	}
	if code, _ := do(t, mux, http.MethodGet, "/api/mission/get?owner=other&id="+id, ""); code == http.StatusOK {
		t.Fatal("other owner read mission")
	}

	job, err := m.Start("codex", sessionID, []string{"true"}, engine.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("job did not finish")
	}
	for _, step := range []string{"step_1", "step_2"} {
		body := `{"owner":"codex","mission_id":"` + id + `","step_id":"` + step + `","step_status":"passed","job_id":"` + job.ID + `"}`
		if code, out := do(t, mux, http.MethodPost, "/api/mission/update", body); code != http.StatusOK {
			t.Fatalf("update %s code=%d body=%v", step, code, out)
		}
	}
	if code, out := do(t, mux, http.MethodPost, "/api/mission/update", `{"owner":"codex","mission_id":"`+id+`","status":"succeeded","summary":"verified"}`); code != http.StatusOK || out["status"] != mission.StatusSucceeded {
		t.Fatalf("finish code=%d body=%v", code, out)
	}
	code, report := do(t, mux, http.MethodGet, "/api/mission/report?owner=codex&id="+id, "")
	if code != http.StatusOK || !strings.Contains(report["report"].(string), job.ID) || report["sha256"] == "" {
		t.Fatalf("report code=%d body=%v", code, report)
	}
}

func TestAPIAgentConnectAndStatus(t *testing.T) {
	mux, _ := newTestServer(t)
	if code, _ := do(t, mux, "POST", "/api/agent/connect", `{"agent":"claude-code"}`); code != 200 {
		t.Fatalf("connect code=%d", code)
	}
	_, st := doOperator(t, mux, "GET", "/api/status", "")
	agents, _ := st["agents"].([]any)
	found := false
	for _, a := range agents {
		if m, ok := a.(map[string]any); ok && m["id"] == "claude-code" {
			found = true
			if m["connections"].(float64) < 1 {
				t.Fatalf("connections not counted: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("agent not in status: %v", st["agents"])
	}
	// status must also expose servers + agents keys (dashboard depends on them)
	if _, ok := st["servers"]; !ok {
		t.Fatalf("status missing servers key")
	}
	if persistence, ok := st["persistence"].(map[string]any); !ok || persistence["healthy"] != true {
		t.Fatalf("status missing healthy persistence state: %v", st["persistence"])
	}
}

func TestStatusReportsRuntimeDashboardURL(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	s := New(m, nil, nil, nil, nil, nil, "test")
	s.SetDashboardURL("http://127.0.0.1:9876/")
	_, status := doOperator(t, s.Mux(), http.MethodGet, "/api/status", "")
	if got := status["dashboard_url"]; got != "http://127.0.0.1:9876/" {
		t.Fatalf("dashboard_url = %v", got)
	}
}

func TestStatusReportsPersistenceFailure(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.EnablePersistence(filepath.Join(blocker, "registry.json")); err == nil {
		t.Fatal("EnablePersistence unexpectedly succeeded")
	}
	s := New(m, nil, nil, nil, nil, nil, "test")
	_, status := doOperator(t, s.Mux(), http.MethodGet, "/api/status", "")
	persistence, ok := status["persistence"].(map[string]any)
	if !ok || persistence["enabled"] != true || persistence["healthy"] != false || persistence["error"] == "" {
		t.Fatalf("persistence status = %v", status["persistence"])
	}
}

func TestFleetRunRedactsReturnedStreams(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	if err := m.Redactor().AddLiteral("fleet-secret"); err != nil {
		t.Fatal(err)
	}
	fl := fleet.New([]fleet.Server{{Name: "web1", Host: "host", User: "ops"}}, secretRunner{}, 1)
	mux := New(m, nil, nil, fl, nil, nil, "test").Mux()
	code, out := do(t, mux, http.MethodPost, "/api/fleet/run", `{"owner":"agent","command":["true"],"servers":["web1"]}`)
	if code != http.StatusOK {
		t.Fatalf("fleet_run = %d, %v", code, out)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fleet-secret") {
		t.Fatalf("fleet response leaked secret: %s", encoded)
	}
}

func TestAPIServerAddRequiresVaultThenWorks(t *testing.T) {
	mux, _ := newTestServer(t)
	// adding with a secret before unlocking the vault must fail
	if code, out := doOperator(t, mux, "POST", "/api/servers/add", `{"name":"web1","host":"10.0.0.1","user":"deploy","secret":"pw"}`); code == 200 {
		t.Fatalf("expected failure before vault unlock, got %v", out)
	}
	// unlock (creates the vault on first use)
	if code, _ := doOperator(t, mux, "POST", "/api/vault/unlock", `{"passphrase":"pw"}`); code != 200 {
		t.Fatalf("unlock failed")
	}
	if code, out := doOperator(t, mux, "POST", "/api/servers/add", `{"name":"web1","host":"10.0.0.1","user":"deploy","secret":"pw","tags":["web"]}`); code != 200 {
		t.Fatalf("add failed: %v", out)
	}
	_, st := doOperator(t, mux, "GET", "/api/status", "")
	servers, _ := st["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("server not listed: %v", servers)
	}
	// connectivity test (mock runner returns ok)
	code, out := doOperator(t, mux, "POST", "/api/servers/test", `{"name":"web1"}`)
	if code != 200 || out["status"] != "ok" {
		t.Fatalf("server test = %d %v", code, out)
	}
}

func TestAPITokenIdentityNonSpoofable(t *testing.T) {
	mux, m := newTestServer(t)
	m.SetAgentTokens(map[string]string{"tok-123": "claude-code"})

	// claim owner "evil" but present claude-code's token
	r := httptest.NewRequest("POST", "/api/exec/run", strings.NewReader(`{"owner":"evil","command":["echo","hi"]}`))
	r.Header.Set("X-Termada-Agent-Token", "tok-123")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	_, st := doOperator(t, mux, "GET", "/api/status", "")
	agents, _ := st["agents"].([]any)
	ids := map[string]bool{}
	for _, a := range agents {
		if m, ok := a.(map[string]any); ok {
			ids[m["id"].(string)] = true
		}
	}
	if !ids["claude-code"] || ids["evil"] {
		t.Fatalf("identity not enforced: token should map to claude-code, not evil; agents=%v", st["agents"])
	}
}

func TestAPIPrincipalFailsClosedAndScopesSessions(t *testing.T) {
	mux, m := newTestServer(t)
	m.SetAgentTokens(map[string]string{"victim-token": "victim", "attacker-token": "attacker"})

	for name, token := range map[string]string{"missing": "", "unknown": "bogus-token"} {
		t.Run(name+" token cannot claim protected id", func(t *testing.T) {
			code, _ := doWithAgentToken(t, mux, "POST", "/api/exec/run", `{"owner":"victim","command":["true"]}`, token)
			if code == http.StatusOK {
				t.Fatal("protected identity accepted without its configured token")
			}
		})
	}
	if code, _ := do(t, mux, "GET", "/api/exec/list", ""); code == http.StatusOK {
		t.Fatal("anonymous request selected the empty operator owner")
	}

	code, created := doWithAgentToken(t, mux, "POST", "/api/session/create", `{"owner":"spoofed"}`, "victim-token")
	if code != http.StatusOK {
		t.Fatalf("victim session create = %d %v", code, created)
	}
	sid, _ := created["session_id"].(string)
	code, listed := doWithAgentToken(t, mux, "GET", "/api/session/list?owner=victim", "", "attacker-token")
	if code != http.StatusOK {
		t.Fatalf("attacker list = %d %v", code, listed)
	}
	if sessions, _ := listed["sessions"].([]any); len(sessions) != 0 {
		t.Fatalf("attacker saw victim sessions: %v", sessions)
	}
	code, _ = doWithAgentToken(t, mux, "POST", "/api/exec/run", `{"owner":"victim","session":"`+sid+`","command":["true"]}`, "attacker-token")
	if code == http.StatusOK {
		t.Fatal("attacker token ran in victim session")
	}
	path := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(path, []byte("victim-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileBody, _ := json.Marshal(map[string]any{"owner": "victim", "session": sid, "path": path})
	if code, _ := doWithAgentToken(t, mux, "POST", "/api/file/read", string(fileBody), "attacker-token"); code == http.StatusOK {
		t.Fatal("attacker token read through victim session")
	}
	if code, _ := doWithAgentToken(t, mux, "POST", "/api/session/close", `{"owner":"victim","session_id":"`+sid+`"}`, "attacker-token"); code == http.StatusOK {
		t.Fatal("attacker token closed victim session")
	}
	code, _ = doWithAgentToken(t, mux, "POST", "/api/session/write", `{"owner":"attacker","session_id":"`+sid+`","input":"echo policy-bypass","human":true}`, "attacker-token")
	if code == http.StatusOK {
		t.Fatal("agent used human=true to write directly to a session PTY")
	}
}

func TestAPIHumanFlagDoesNotBypassJobOwnership(t *testing.T) {
	mux, m := newTestServer(t)
	m.SetAgentTokens(map[string]string{"victim-token": "victim", "attacker-token": "attacker"})
	job, err := m.Start("victim", "", []string{"sleep", "5"}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Kill("victim", job.ID) })
	body := `{"owner":"victim","job_id":"` + job.ID + `","input":"x","human":true}`
	if code, _ := doWithAgentToken(t, mux, "POST", "/api/exec/write", body, "attacker-token"); code == http.StatusOK {
		t.Fatal("attacker used human=true to bypass job ownership")
	}
}

func TestAPIRejectsMalformedUnknownAndOversizedJSON(t *testing.T) {
	mux, m := newTestServer(t)
	for name, body := range map[string]string{
		"malformed": `{"owner":"agent"`,
		"unknown":   `{"owner":"agent","bogus":true}`,
		"multiple":  `{"owner":"agent"} {"owner":"other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if code, _ := do(t, mux, "POST", "/api/session/create", body); code == http.StatusOK {
				t.Fatalf("invalid JSON was accepted: %s", body)
			}
		})
	}
	if got := len(m.ListSessionsFor("agent")); got != 0 {
		t.Fatalf("invalid requests created %d sessions", got)
	}

	path := filepath.Join(t.TempDir(), "must-not-exist")
	body, err := json.Marshal(map[string]any{
		"owner": "agent", "path": path,
		"content": strings.Repeat("x", maxRequestBodyBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := do(t, mux, "POST", "/api/file/write", string(body)); code == http.StatusOK {
		t.Fatal("oversized request body was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversized request modified the filesystem: %v", err)
	}
}

func TestExecStreamDrainsCappedTerminalOutput(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.MaxOutputBytes = 4
	m := engine.NewManager(cfg)
	t.Cleanup(m.Shutdown)
	job, err := m.Start("agent", "", []string{"printf", "0123456789"}, engine.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}

	s := New(m, nil, nil, nil, nil, nil, "test")
	req := httptest.NewRequest(http.MethodGet, "/api/exec/stream?job_id="+job.ID, nil)
	req = WithOperatorPrincipal(req)
	rec := httptest.NewRecorder()
	s.hExecStream(rec, req)
	body := rec.Body.String()
	for _, page := range []string{"0123", "4567", "89"} {
		if !strings.Contains(body, page) {
			t.Fatalf("terminal stream did not drain page %q: %s", page, body)
		}
	}
	if !strings.Contains(body, "id: ") {
		t.Fatalf("terminal stream omitted resumable SSE ids: %s", body)
	}

	first, err := m.PollFor("", job.ID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	resumeReq := httptest.NewRequest(http.MethodGet, "/api/exec/stream?job_id="+job.ID+"&cursor=0", nil)
	resumeReq.Header.Set("Last-Event-ID", first.NextCursor)
	resumeReq = WithOperatorPrincipal(resumeReq)
	resumeRec := httptest.NewRecorder()
	s.hExecStream(resumeRec, resumeReq)
	resumed := resumeRec.Body.String()
	if strings.Contains(resumed, "0123") || !strings.Contains(resumed, "4567") || !strings.Contains(resumed, "89") {
		t.Fatalf("resumed terminal stream replayed or lost output: %s", resumed)
	}
}

func TestExecStreamReportsRetentionGap(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.OutputRetentionBytes = 4
	cfg.MaxOutputBytes = 4
	m := engine.NewManager(cfg)
	t.Cleanup(m.Shutdown)
	job, err := m.Start("agent", "", []string{"printf", "0123456789"}, engine.ModeForeground)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}
	s := New(m, nil, nil, nil, nil, nil, "test")
	req := httptest.NewRequest(http.MethodGet, "/api/exec/stream?job_id="+job.ID, nil)
	req.Header.Set("Last-Event-ID", "1")
	req = WithOperatorPrincipal(req)
	rec := httptest.NewRecorder()
	s.hExecStream(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `"gap":true`) || !strings.Contains(body, "6789") {
		t.Fatalf("expired cursor did not surface an output gap: %s", body)
	}
}

func TestSessionStreamDrainsCappedClosedOutput(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.MaxOutputBytes = 4
	m := engine.NewManager(cfg)
	t.Cleanup(m.Shutdown)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"printf", "0123456789"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}
	if err := m.SessionWriteInput(sess.ID, "exit", true, true); err != nil {
		t.Fatalf("close session shell: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := m.SessionTail(sess.ID, "")
		if err != nil {
			t.Fatalf("read session tail: %v", err)
		}
		if res.Closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session shell did not close")
		}
		time.Sleep(10 * time.Millisecond)
	}

	s := New(m, nil, nil, nil, nil, nil, "test")
	req := httptest.NewRequest(http.MethodGet, "/api/session/stream?session_id="+sess.ID, nil)
	req = WithOperatorPrincipal(req)
	rec := httptest.NewRecorder()
	s.hSessionStream(rec, req)
	body := rec.Body.String()
	for _, page := range []string{"0123", "4567", "89"} {
		if !strings.Contains(body, page) {
			t.Fatalf("closed session stream did not drain page %q: %s", page, body)
		}
	}
	if !strings.Contains(body, `"done":true`) {
		t.Fatalf("closed session stream omitted terminal event: %s", body)
	}
}

func TestTerminalStreamsExposePromptBeforeInput(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"bash", "-c", "printf 'Generate secrets? [y/N] '; read -r answer; sleep 0.3; printf 'answer=%s\\n' \"$answer\""}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}

	s := New(m, nil, nil, nil, nil, nil, "test")
	for name, path := range map[string]string{
		"job":     "/api/exec/stream?job_id=" + job.ID,
		"session": "/api/session/stream?session_id=" + sess.ID,
	} {
		t.Run(name, func(t *testing.T) {
			event := firstSSEEvent(t, s, path)
			if awaiting, _ := event["awaiting_input"].(bool); !awaiting {
				t.Fatalf("prompt event is not awaiting input: %v", event)
			}
			if prompt, _ := event["prompt"].(string); prompt != "Generate secrets? [y/N]" {
				t.Fatalf("prompt = %q, want exact question", prompt)
			}
		})
	}

	if err := m.SessionWriteInput(sess.ID, "n", false, true); err != nil {
		t.Fatalf("type prompt answer: %v", err)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("prompt disappeared before Enter")
	}
	if err := m.SessionWriteInput(sess.ID, "\r", false, true); err != nil {
		t.Fatalf("submit prompt answer: %v", err)
	}
	if job.Snapshot().AwaitingInput {
		t.Fatal("prompt remained active after Enter")
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after input")
	}
	idleEvent := firstSSEEvent(t, s, "/api/session/stream?session_id="+sess.ID)
	if awaiting, _ := idleEvent["awaiting_input"].(bool); awaiting || idleEvent["prompt"] != "" {
		t.Fatalf("initial idle stream state retained a stale prompt: %v", idleEvent)
	}
}

func TestSessionStreamStateOnlyEventCarriesCurrentJobID(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"bash", "-c", "printf 'Approve? '; read -r answer"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	tail, err := m.SessionTail(sess.ID, "")
	if err != nil {
		t.Fatalf("session tail: %v", err)
	}
	if tail.JobID != job.ID || !tail.AwaitingInput || tail.Prompt != "Approve?" {
		t.Fatalf("atomic session state = %+v, want current job prompt", tail)
	}

	s := New(m, nil, nil, nil, nil, nil, "test")
	event := firstSSEEvent(t, s, "/api/session/stream?session_id="+sess.ID+"&cursor="+tail.NextCursor)
	if chunk, _ := event["chunk"].(string); chunk != "" {
		t.Fatalf("state-only event unexpectedly carried output: %v", event)
	}
	if event["job_id"] != job.ID || event["awaiting_input"] != true || event["prompt"] != "Approve?" {
		t.Fatalf("state-only event lost current job identity: %v", event)
	}

	if err := m.SessionWriteInput(sess.ID, "n", true, true); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after input")
	}
}

func TestStatusExposesExecutionContext(t *testing.T) {
	mux, m := newTestServer(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"bash", "-c", "printf 'Value: '; read -r value"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = m.Kill("", job.ID) })
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}

	code, status := doOperator(t, mux, http.MethodGet, "/api/status", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d %v", code, status)
	}
	sessions, _ := status["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v, want one", sessions)
	}
	session, _ := sessions[0].(map[string]any)
	if session["target"] != "local" || session["current_job_id"] != job.ID {
		t.Fatalf("session context = %v", session)
	}
	if created, _ := session["created_unix"].(float64); created <= 0 {
		t.Fatalf("session created_unix = %v", session["created_unix"])
	}
	if createdMS, _ := session["created_unix_ms"].(float64); createdMS <= 0 || int64(createdMS)/1000 != int64(session["created_unix"].(float64)) {
		t.Fatalf("session millisecond timestamp = %v", session)
	}

	jobs, _ := status["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v, want one", jobs)
	}
	jobInfo, _ := jobs[0].(map[string]any)
	if jobInfo["target"] != "local" || jobInfo["mode"] != engine.ModeForeground {
		t.Fatalf("job context = %v", jobInfo)
	}
	if created, _ := jobInfo["created_unix"].(float64); created <= 0 {
		t.Fatalf("job created_unix = %v", jobInfo["created_unix"])
	}
	if started, _ := jobInfo["started_unix"].(float64); started <= 0 {
		t.Fatalf("job started_unix = %v", jobInfo["started_unix"])
	}
	for secondsField, millisecondsField := range map[string]string{
		"created_unix": "created_unix_ms",
		"started_unix": "started_unix_ms",
	} {
		seconds, _ := jobInfo[secondsField].(float64)
		milliseconds, _ := jobInfo[millisecondsField].(float64)
		if milliseconds <= 0 || int64(milliseconds)/1000 != int64(seconds) {
			t.Fatalf("job timestamp %s/%s = %v", secondsField, millisecondsField, jobInfo)
		}
	}
	if _, ok := jobInfo["ended_unix"]; ok {
		t.Fatalf("running job unexpectedly has ended_unix: %v", jobInfo)
	}
	if err := m.SessionWriteInput(sess.ID, "done", true, true); err != nil {
		t.Fatalf("complete job: %v", err)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after input")
	}
	code, listed := doOperator(t, mux, http.MethodGet, "/api/exec/list?filter=all", "")
	if code != http.StatusOK {
		t.Fatalf("list jobs = %d %v", code, listed)
	}
	finishedJobs, _ := listed["jobs"].([]any)
	if len(finishedJobs) != 1 {
		t.Fatalf("finished jobs = %v, want one", finishedJobs)
	}
	finished, _ := finishedJobs[0].(map[string]any)
	if ended, _ := finished["ended_unix"].(float64); ended <= 0 {
		t.Fatalf("finished job ended_unix = %v", finished["ended_unix"])
	}
	if endedMS, _ := finished["ended_unix_ms"].(float64); endedMS <= 0 || int64(endedMS)/1000 != int64(finished["ended_unix"].(float64)) {
		t.Fatalf("finished job millisecond timestamp = %v", finished)
	}
}

func TestStatusExposesPendingApprovalContext(t *testing.T) {
	mux, m := newTestServer(t)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"review": {Confirm: []string{"echo*"}},
	}), map[string]string{"agent": "review"})
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"echo", "needs approval"}, engine.ModeBackground)
	if err != nil {
		t.Fatalf("start confirmation: %v", err)
	}
	t.Cleanup(func() { _ = m.Deny(job.Snapshot().ConfirmationID, "test cleanup") })

	code, status := doOperator(t, mux, http.MethodGet, "/api/status", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d %v", code, status)
	}
	pendingList, _ := status["pending"].([]any)
	if len(pendingList) != 1 {
		t.Fatalf("pending = %v, want one", pendingList)
	}
	pending, _ := pendingList[0].(map[string]any)
	if pending["target"] != "local" || pending["reason"] != "matched confirm rule" || pending["mode"] != engine.ModeBackground {
		t.Fatalf("pending context = %v", pending)
	}
	requested, _ := pending["requested_unix"].(float64)
	expires, _ := pending["expires_unix"].(float64)
	if requested <= 0 || expires <= requested {
		t.Fatalf("pending timing requested=%v expires=%v", requested, expires)
	}
	requestedMS, _ := pending["requested_unix_ms"].(float64)
	expiresMS, _ := pending["expires_unix_ms"].(float64)
	if requestedMS <= 0 || expiresMS <= requestedMS || int64(requestedMS)/1000 != int64(requested) || int64(expiresMS)/1000 != int64(expires) {
		t.Fatalf("pending millisecond timing = %v", pending)
	}
}

func TestAPISessionWriteSecretIsRedactedEverywhere(t *testing.T) {
	mux, m, auditLog, auditPath := newAuditedTestServer(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"bash", "-c", "printf 'Secret: '; read -r value; printf 'seen=%s\\n' \"$value\""}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	const secret = "session-write-secret-value"
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	body, _ := json.Marshal(map[string]any{
		"session_id": sess.ID,
		"input":      secret,
		"secret":     true,
	})
	if code, out := doOperatorSource(t, mux, "dashboard", http.MethodPost, "/api/session/write", string(body)); code != http.StatusOK {
		t.Fatalf("session write = %d %v", code, out)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after secret input")
	}

	jobOutput, err := m.PollFor("", job.ID, "", true)
	if err != nil {
		t.Fatalf("poll job: %v", err)
	}
	sessionOutput, err := m.SessionTail(sess.ID, "")
	if err != nil {
		t.Fatalf("tail session: %v", err)
	}
	for surface, output := range map[string]string{
		"job":     jobOutput.StdoutChunk,
		"session": sessionOutput.Chunk,
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("%s output leaked secret: %q", surface, output)
		}
		if !strings.Contains(output, "REDACTED") {
			t.Fatalf("%s output omitted redaction marker: %q", surface, output)
		}
	}
	events, err := json.Marshal(m.Bus().Recent(100))
	if err != nil {
		t.Fatalf("encode events: %v", err)
	}
	if strings.Contains(string(events), secret) {
		t.Fatalf("event bus leaked secret: %s", events)
	}
	records, err := auditLog.Tail(100)
	if err != nil {
		t.Fatalf("tail audit: %v", err)
	}
	var inputRecord *audit.Record
	for i := range records {
		if records[i].Type == bus.EvHumanInputAuthorized {
			inputRecord = &records[i]
		}
	}
	if inputRecord == nil {
		t.Fatalf("audit omitted %s: %+v", bus.EvHumanInputAuthorized, records)
	}
	if inputRecord.SessionID != sess.ID || inputRecord.JobID != job.ID || inputRecord.Data["actor"] != "dashboard" || inputRecord.Data["source"] != "dashboard" {
		t.Fatalf("input attribution = %+v", inputRecord)
	}
	if inputRecord.Data["scope"] != "job" || inputRecord.Data["delivery"] != "not_recorded" || inputRecord.Data["secret"] != true || inputRecord.Data["append_newline"] != true || int(inputRecord.Data["byte_count"].(float64)) != len(secret) {
		t.Fatalf("input metadata = %+v", inputRecord.Data)
	}
	if _, ok := inputRecord.Data["input"]; ok {
		t.Fatalf("audit metadata includes raw input field: %+v", inputRecord.Data)
	}
	auditBytes, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if strings.Contains(string(auditBytes), secret) {
		t.Fatalf("audit file leaked secret: %s", auditBytes)
	}
}

func TestAPISecretInputDoesNotRedactOwnAuthorizationMetadata(t *testing.T) {
	mux, m, auditLog, _ := newAuditedTestServer(t)
	job, err := m.Start("agent", "", []string{"bash", "-c", "printf 'Value: '; read -r value"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}

	// The literal intentionally collides with both a metadata field name and the
	// trusted principal. Authorization must be durable before the literal becomes
	// active, or the audit redactor destroys its own attribution envelope.
	const secret = "actor"
	body, _ := json.Marshal(map[string]any{"job_id": job.ID, "input": secret, "secret": true})
	if code, out := doOperatorSource(t, mux, secret, http.MethodPost, "/api/exec/write", string(body)); code != http.StatusOK {
		t.Fatalf("exec write = %d %v", code, out)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after secret input")
	}

	records, err := auditLog.Tail(100)
	if err != nil {
		t.Fatalf("tail audit: %v", err)
	}
	var inputRecord *audit.Record
	for i := range records {
		if records[i].Type == bus.EvHumanInputAuthorized {
			inputRecord = &records[i]
		}
	}
	if inputRecord == nil {
		t.Fatalf("audit omitted %s: %+v", bus.EvHumanInputAuthorized, records)
	}
	if inputRecord.Data["actor"] != secret || inputRecord.Data["source"] != secret {
		t.Fatalf("secret input self-redacted authorization attribution: %+v", inputRecord.Data)
	}
	if inputRecord.Data["scope"] != "job" || inputRecord.Data["secret"] != true {
		t.Fatalf("secret input self-redacted authorization metadata: %+v", inputRecord.Data)
	}
}

func TestAPIExecWriteAuditsMetadataButNeverPlaintext(t *testing.T) {
	mux, m, auditLog, auditPath := newAuditedTestServer(t)
	job, err := m.Start("agent", "", []string{"bash", "-c", "printf 'Reply: '; read -r value"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	const reply = "ordinary-reply-never-audit"
	body, _ := json.Marshal(map[string]any{"job_id": job.ID, "input": reply})
	if code, out := doOperatorSource(t, mux, "cli", http.MethodPost, "/api/exec/write", string(body)); code != http.StatusOK {
		t.Fatalf("exec write = %d %v", code, out)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after input")
	}

	records, err := auditLog.Tail(100)
	if err != nil {
		t.Fatalf("tail audit: %v", err)
	}
	var inputRecord *audit.Record
	for i := range records {
		if records[i].Type == bus.EvHumanInputAuthorized {
			inputRecord = &records[i]
		}
	}
	if inputRecord == nil || inputRecord.JobID != job.ID || inputRecord.SessionID != job.SessionID {
		t.Fatalf("exec input attribution = %+v", inputRecord)
	}
	if inputRecord.Data["actor"] != "cli" || inputRecord.Data["source"] != "cli" || inputRecord.Data["scope"] != "job" || inputRecord.Data["delivery"] != "not_recorded" || inputRecord.Data["secret"] != false {
		t.Fatalf("exec input metadata = %+v", inputRecord.Data)
	}
	auditBytes, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if strings.Contains(string(auditBytes), reply) {
		t.Fatalf("audit file recorded ordinary input plaintext: %s", auditBytes)
	}
}

func TestAPIIdleSessionInputIsMetadataAuditedWithoutCommand(t *testing.T) {
	mux, m, auditLog, auditPath := newAuditedTestServer(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	const command = "printf raw-idle-audit-marker"
	body, _ := json.Marshal(map[string]any{"session_id": sess.ID, "input": command})
	if code, out := doOperatorSource(t, mux, "dashboard", http.MethodPost, "/api/session/write", string(body)); code != http.StatusOK {
		t.Fatalf("idle session write = %d %v", code, out)
	}

	records, err := auditLog.Tail(100)
	if err != nil {
		t.Fatalf("tail audit: %v", err)
	}
	var inputRecord *audit.Record
	for i := range records {
		if records[i].Type == bus.EvHumanInputAuthorized {
			inputRecord = &records[i]
		}
	}
	if inputRecord == nil {
		t.Fatalf("audit omitted idle terminal input: %+v", records)
	}
	if inputRecord.SessionID != sess.ID || inputRecord.JobID != "" || inputRecord.Data["scope"] != "session_terminal" || inputRecord.Data["delivery"] != "not_recorded" {
		t.Fatalf("idle terminal attribution = %+v", inputRecord)
	}
	if inputRecord.Data["actor"] != "dashboard" || inputRecord.Data["source"] != "dashboard" || int(inputRecord.Data["byte_count"].(float64)) != len(command) {
		t.Fatalf("idle terminal metadata = %+v", inputRecord.Data)
	}
	auditBytes, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if strings.Contains(string(auditBytes), command) || strings.Contains(string(auditBytes), "raw-idle-audit-marker") {
		t.Fatalf("audit file recorded raw idle command: %s", auditBytes)
	}
}

func TestAPIApprovalActorComesFromAuthenticatedPrincipal(t *testing.T) {
	for _, action := range []string{"approve", "deny"} {
		t.Run(action, func(t *testing.T) {
			mux, m, auditLog, auditPath := newAuditedTestServer(t)
			m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
				"review": {Confirm: []string{"echo*"}},
			}), map[string]string{"agent": "review"})
			job, err := m.Start("agent", "", []string{"echo", action}, engine.ModeForeground)
			if err != nil {
				t.Fatalf("start confirmation: %v", err)
			}
			body := `{"confirmation_id":"` + job.Snapshot().ConfirmationID + `","by":"spoofed-body-actor"}`
			code, out := doOperatorSource(t, mux, "dashboard", http.MethodPost, "/api/"+action, body)
			if code != http.StatusOK {
				t.Fatalf("%s = %d %v", action, code, out)
			}
			select {
			case <-job.Done():
			case <-time.After(5 * time.Second):
				t.Fatalf("%s job did not finish", action)
			}

			records, err := auditLog.Tail(100)
			if err != nil {
				t.Fatalf("tail audit: %v", err)
			}
			var resolved *audit.Record
			for i := range records {
				if records[i].Type == bus.EvConfirmResolved {
					resolved = &records[i]
				}
			}
			if resolved == nil || resolved.Data["by"] != "dashboard" || resolved.SessionID == "" || resolved.JobID != job.ID {
				t.Fatalf("resolved approval attribution = %+v", resolved)
			}
			auditBytes, err := os.ReadFile(auditPath)
			if err != nil {
				t.Fatalf("read audit: %v", err)
			}
			if strings.Contains(string(auditBytes), "spoofed-body-actor") {
				t.Fatalf("request body spoofed audit actor: %s", auditBytes)
			}
		})
	}
}

func TestAPIHumanInputFailsClosedWhenAuditIsUnhealthy(t *testing.T) {
	mux, m, auditLog, _ := newAuditedTestServer(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"bash", "-c", "printf 'Value: '; read -r value"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	m.SetAuditHealth(func() bool { return false })
	body := `{"session_id":"` + sess.ID + `","input":"must-not-reach-pty"}`
	if code, _ := doOperatorSource(t, mux, "dashboard", http.MethodPost, "/api/session/write", body); code == http.StatusOK {
		t.Fatal("human input succeeded while audit was unhealthy")
	}
	time.Sleep(100 * time.Millisecond)
	if !job.Snapshot().AwaitingInput {
		t.Fatal("audit-unhealthy input reached PTY")
	}
	m.SetAuditHealth(auditLog.Healthy)
	cleanup := `{"session_id":"` + sess.ID + `","input":"cleanup"}`
	if code, out := doOperatorSource(t, mux, "dashboard", http.MethodPost, "/api/session/write", cleanup); code != http.StatusOK {
		t.Fatalf("cleanup write = %d %v", code, out)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after cleanup")
	}
}

func TestAPIHumanInputPreWriteAuditFailureDoesNotReachPTY(t *testing.T) {
	mux, m, auditLog, _ := newAuditedTestServer(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"bash", "-c", "printf 'Value: '; read -r value"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	cancelFailure := m.Bus().SubscribeReliable(func(event bus.Event) error {
		if event.Type == bus.EvHumanInputAuthorized {
			return errors.New("forced authorization audit failure")
		}
		return nil
	})
	const failedSecret = "failed-audit-secret-literal"
	body := `{"session_id":"` + sess.ID + `","input":"` + failedSecret + `","secret":true}`
	if code, _ := doOperatorSource(t, mux, "dashboard", http.MethodPost, "/api/session/write", body); code == http.StatusOK {
		cancelFailure()
		t.Fatal("input succeeded after pre-write audit authorization failed")
	}
	cancelFailure()
	time.Sleep(100 * time.Millisecond)
	if !job.Snapshot().AwaitingInput {
		t.Fatal("pre-write audit failure still allowed PTY input")
	}
	// The durable envelope may have reached another reliable sink before one sink
	// failed, but it explicitly does not claim delivery.
	records, err := auditLog.Tail(100)
	if err != nil {
		t.Fatalf("tail audit: %v", err)
	}
	foundAuthorization := false
	for _, record := range records {
		if record.Type == bus.EvHumanInputAuthorized {
			foundAuthorization = true
			if record.Data["delivery"] != "not_recorded" {
				t.Fatalf("failed authorization claimed delivery: %+v", record)
			}
		}
	}
	if !foundAuthorization {
		t.Fatal("healthy audit sink did not retain the attempted authorization")
	}
	if got := m.Redactor().Redact(failedSecret); got != failedSecret {
		t.Fatalf("failed audit authorization consumed secret literal capacity: %q", got)
	}
	cleanup := `{"session_id":"` + sess.ID + `","input":"cleanup"}`
	if code, out := doOperatorSource(t, mux, "dashboard", http.MethodPost, "/api/session/write", cleanup); code != http.StatusOK {
		t.Fatalf("cleanup write = %d %v", code, out)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after cleanup")
	}
}

func TestAPIHumanInputAuthorizationPrecedesFastCompletion(t *testing.T) {
	mux, m, auditLog, _ := newAuditedTestServer(t)
	job, err := m.Start("agent", "", []string{"bash", "-c", "printf 'Go: '; read -r value"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	// There is intentionally no post-write input event. A failure on the first
	// event caused by the completed process must not turn a successful input API
	// response into a retriable error after the PTY side effect.
	cancelFailure := m.Bus().SubscribeReliable(func(event bus.Event) error {
		if event.Type == bus.EvJobFinished {
			return errors.New("forced post-write audit failure")
		}
		return nil
	})
	t.Cleanup(cancelFailure)
	body := `{"job_id":"` + job.ID + `","input":"done"}`
	if code, out := doOperatorSource(t, mux, "dashboard", http.MethodPost, "/api/exec/write", body); code != http.StatusOK {
		t.Fatalf("exec write reported a retriable error after PTY write: %d %v", code, out)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("fast job did not finish")
	}

	var records []audit.Record
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records, err = auditLog.Tail(100)
		if err != nil {
			t.Fatalf("tail audit: %v", err)
		}
		for _, record := range records {
			if record.Type == bus.EvJobFinished {
				goto haveFinished
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("audit omitted fast job completion")

haveFinished:
	authorizedAt, finishedAt := -1, -1
	humanEvents := 0
	for i, record := range records {
		if strings.HasPrefix(record.Type, "human_input.") {
			humanEvents++
			if record.Type == bus.EvHumanInputAuthorized {
				authorizedAt = i
			}
		}
		if record.Type == bus.EvJobFinished && record.JobID == job.ID {
			finishedAt = i
		}
	}
	if authorizedAt < 0 || finishedAt < 0 || authorizedAt >= finishedAt {
		t.Fatalf("causal audit order authorization=%d finished=%d records=%+v", authorizedAt, finishedAt, records)
	}
	if humanEvents != 1 {
		t.Fatalf("input generated %d human audit events, want authorization envelope only: %+v", humanEvents, records)
	}
}

func TestAPISessionWriteSecretFailsClosedWhenRedactorIsFull(t *testing.T) {
	mux, m := newTestServer(t)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"bash", "-c", "printf 'Value: '; read -r value; printf 'seen=%s\\n' \"$value\""}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !job.Snapshot().AwaitingInput && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !job.Snapshot().AwaitingInput {
		t.Fatal("job did not expose its input prompt")
	}
	if err := m.Redactor().AddLiteral(strings.Repeat("x", 4<<20)); err != nil {
		t.Fatalf("fill redactor: %v", err)
	}
	failedBody := `{"session_id":"` + sess.ID + `","input":"must-not-reach-pty","secret":true}`
	if code, _ := doOperator(t, mux, http.MethodPost, "/api/session/write", failedBody); code == http.StatusOK {
		t.Fatal("secret write succeeded after redactor reached capacity")
	}
	time.Sleep(100 * time.Millisecond)
	if !job.Snapshot().AwaitingInput {
		t.Fatal("failed secret registration still wrote input to the PTY")
	}
	cleanupBody := `{"session_id":"` + sess.ID + `","input":"cleanup"}`
	if code, out := doOperator(t, mux, http.MethodPost, "/api/session/write", cleanupBody); code != http.StatusOK {
		t.Fatalf("cleanup write = %d %v", code, out)
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish after cleanup input")
	}
}

func TestAPIPortForwardsAreOwnerScoped(t *testing.T) {
	mux, m := newTestServer(t)
	m.SetAgentTokens(map[string]string{"victim-token": "victim", "attacker-token": "attacker"})
	m.SetForwardOps(&testForwards{items: map[string]engine.ForwardInfo{}})
	body := `{"owner":"spoofed","server":"prod","remote_host":"127.0.0.1","remote_port":5432}`
	code, out := doWithAgentToken(t, mux, "POST", "/api/forward/start", body, "victim-token")
	if code != http.StatusOK || out["owner"] != "victim" {
		t.Fatalf("victim start = %d %v", code, out)
	}
	code, out = doWithAgentToken(t, mux, "POST", "/api/forward/list", `{"owner":"victim"}`, "attacker-token")
	if code != http.StatusOK {
		t.Fatalf("attacker list = %d %v", code, out)
	}
	if forwards, _ := out["forwards"].([]any); len(forwards) != 0 {
		t.Fatalf("attacker saw victim forwards: %v", forwards)
	}
	if code, _ := doWithAgentToken(t, mux, "POST", "/api/forward/close", `{"owner":"victim","id":"fwd_test"}`, "attacker-token"); code == http.StatusOK {
		t.Fatal("attacker closed victim forward")
	}
	if code, out := doWithAgentToken(t, mux, "POST", "/api/forward/close", `{"owner":"spoofed","id":"fwd_test"}`, "victim-token"); code != http.StatusOK {
		t.Fatalf("victim close = %d %v", code, out)
	}
	seenStart, seenClose := false, false
	for _, event := range m.Bus().Recent(20) {
		seenStart = seenStart || event.Type == "forward.started"
		seenClose = seenClose || event.Type == "forward.closed"
	}
	if !seenStart || !seenClose {
		t.Fatalf("forward lifecycle was not auditable: start=%v close=%v", seenStart, seenClose)
	}
}

func TestAPISessionLifecycle(t *testing.T) {
	mux, _ := newTestServer(t)
	code, out := do(t, mux, "POST", "/api/session/create", `{"owner":"t","workspace":"termada"}`)
	if code != 200 || out["session_id"] == nil {
		t.Fatalf("create session = %d %v", code, out)
	}
	sid := out["session_id"].(string)
	if out["workspace"] != "termada" {
		t.Fatalf("created workspace = %v, want termada", out["workspace"])
	}
	_, st := doOperator(t, mux, "GET", "/api/status", "")
	if sessions, _ := st["sessions"].([]any); len(sessions) != 1 {
		t.Fatalf("session not in status: %v", st["sessions"])
	} else if session, _ := sessions[0].(map[string]any); session["workspace"] != "termada" {
		t.Fatalf("status workspace = %v, want termada", session["workspace"])
	}
	if code, _ := do(t, mux, "POST", "/api/session/close", `{"owner":"t","session_id":"`+sid+`"}`); code != 200 {
		t.Fatalf("close session failed")
	}
}

// fleet_run bypasses the per-job engine path, so without explicit events it is
// invisible in the dashboard and the audit log (both fed from the bus). It must
// publish a start + finished event, attributed to the acting agent, with the
// per-server outcomes.
func TestAPIFleetRunIsObservable(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	b := bus.New(100)
	m.SetBus(b)
	v := vault.New(filepath.Join(t.TempDir(), "v.age"))
	fl := fleet.New([]fleet.Server{{Name: "web1", Host: "h1", User: "u"}, {Name: "web2", Host: "h2", User: "u"}}, okRunner{}, 2)
	mux := New(m, b, nil, fl, v, nil, "test").Mux()

	ch, cancel := b.Subscribe(64)
	defer cancel()

	if code, out := do(t, mux, "POST", "/api/fleet/run", `{"owner":"claude-code","command":["uptime"],"servers":["web1","web2"]}`); code != 200 {
		t.Fatalf("fleet run code=%d body=%v", code, out)
	}

	var start, fin *bus.Event
	deadline := time.After(2 * time.Second)
	for start == nil || fin == nil {
		select {
		case e := <-ch:
			ev := e
			if ev.Type == bus.EvFleetStarted {
				start = &ev
			} else if ev.Type == bus.EvFleetFinished {
				fin = &ev
			}
		case <-deadline:
			t.Fatalf("missing fleet events (start=%v finished=%v)", start != nil, fin != nil)
		}
	}

	if start.AgentID != "claude-code" || fin.AgentID != "claude-code" {
		t.Fatalf("fleet run not attributed to the agent: start=%q fin=%q", start.AgentID, fin.AgentID)
	}
	if start.Message != "uptime" {
		t.Fatalf("start message = %q, want the command line", start.Message)
	}
	results, ok := fin.Data["results"].([]map[string]any)
	if !ok || len(results) != 2 {
		t.Fatalf("finished event must carry per-server outcomes, got: %v", fin.Data["results"])
	}
}

func TestAPIFleetRunRefusesWhenStartAuditFails(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	b := bus.New(10)
	m.SetBus(b)
	cancel := b.SubscribeReliable(func(event bus.Event) error {
		if event.Type == bus.EvFleetStarted {
			return errors.New("disk full")
		}
		return nil
	})
	t.Cleanup(cancel)
	runner := &countingRunner{}
	fl := fleet.New([]fleet.Server{{Name: "web1", Host: "h1", User: "u"}}, runner, 1)
	mux := New(m, b, nil, fl, nil, nil, "test").Mux()
	if code, _ := do(t, mux, "POST", "/api/fleet/run", `{"owner":"agent","command":["uptime"],"servers":["web1"]}`); code == http.StatusOK {
		t.Fatal("fleet run succeeded without a durable start audit record")
	}
	if runner.calls != 0 {
		t.Fatalf("fleet runner called %d times after audit failure", runner.calls)
	}
}

// fleet_run must enforce the agent's policy like local commands do — otherwise an
// agent runs anything on remote servers with no allow/deny/confirm gate. Denied
// and confirm-required commands are refused (confirm fails closed: fleet can't
// request approval yet); allowed commands run.
func TestAPIFleetRunPolicyGated(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	b := bus.New(100)
	m.SetBus(b)
	v := vault.New(filepath.Join(t.TempDir(), "v.age"))
	fl := fleet.New([]fleet.Server{{Name: "web1", Host: "h", User: "u"}}, okRunner{}, 2)
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"ro":   {Allow: []string{"ls"}, Deny: []string{"*"}}, // whitelist: only ls
		"prod": {Confirm: []string{"systemctl*"}},
	}), map[string]string{"ci": "ro", "ops": "prod"})
	mux := New(m, b, nil, fl, v, nil, "test").Mux()

	// not on the whitelist → denied
	if code, _ := do(t, mux, "POST", "/api/fleet/run", `{"owner":"ci","command":["rm","-rf","/"],"servers":["web1"]}`); code != 422 {
		t.Fatalf("denied fleet command => %d, want 422", code)
	}
	// confirm-required → fail closed (refused), not run unsupervised
	if code, _ := do(t, mux, "POST", "/api/fleet/run", `{"owner":"ops","command":["systemctl","stop","api"],"servers":["web1"]}`); code != 422 {
		t.Fatalf("confirm-required fleet command => %d, want 422 (fail closed)", code)
	}
	// allowed by the whitelist → runs
	if code, out := do(t, mux, "POST", "/api/fleet/run", `{"owner":"ci","command":["ls"],"servers":["web1"]}`); code != 200 {
		t.Fatalf("allowed fleet command => %d %v, want 200", code, out)
	}
}

func TestAPIPolicyCRUD(t *testing.T) {
	mux, m := newTestServer(t)
	// One config-defined policy "prod"; agentX is assigned the (to-be-created)
	// managed policy "ci".
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"prod": {Deny: []string{"rm -rf /"}},
	}), map[string]string{"agentX": "ci"})

	// Create a managed policy via the dashboard endpoint.
	if code, out := doOperator(t, mux, "POST", "/api/policies/set", `{"name":"ci","allow":["ls"],"deny":["sudo*"]}`); code != 200 {
		t.Fatalf("set ci => %d %v", code, out)
	}
	// It reports as managed (editable); the config policy does not.
	_, pol := doOperator(t, mux, "GET", "/api/policies", "")
	managed, _ := pol["managed"].(map[string]any)
	if managed["ci"] != true || managed["prod"] == true {
		t.Fatalf("managed flags wrong: %v", pol["managed"])
	}

	// A config-defined policy is read-only via the API.
	if code, _ := doOperator(t, mux, "POST", "/api/policies/set", `{"name":"prod","allow":["*"]}`); code == 200 {
		t.Fatal("editing a config-defined policy should be refused")
	}
	// Removing a policy still assigned to an agent is refused (no silent allow-all).
	if code, _ := doOperator(t, mux, "POST", "/api/policies/remove", `{"name":"ci"}`); code == 200 {
		t.Fatal("removing an assigned policy should be refused")
	}
	// Reassign the agent, then ci can be removed.
	m.SetPolicy(m.Policy(), map[string]string{"agentX": "prod"})
	if code, out := doOperator(t, mux, "POST", "/api/policies/remove", `{"name":"ci"}`); code != 200 {
		t.Fatalf("removing an unassigned managed policy => %d %v", code, out)
	}

	// Editing a managed policy through the dashboard preserves its auto_answer
	// rules (the form can't express them, so they must not be silently dropped).
	if err := m.Policy().Set("aa", policy.Policy{
		Confirm:    []string{"deploy*"},
		AutoAnswer: []policy.AutoAnswer{{Match: "continue?", Send: "yes"}},
	}); err != nil {
		t.Fatalf("seed aa: %v", err)
	}
	if code, _ := doOperator(t, mux, "POST", "/api/policies/set", `{"name":"aa","confirm":["deploy*","release*"]}`); code != 200 {
		t.Fatal("editing managed aa should succeed")
	}
	aa := m.Policy().Policies()["aa"].AutoAnswer
	if len(aa) != 1 || aa[0].Send != "yes" {
		t.Fatalf("auto_answer dropped on edit: %v", aa)
	}
}

// A plugin call is an arbitrary executable, so it must pass the agent's policy
// like exec/fleet: a denied plugin is refused, and a confirm-required one fails
// closed (human approval isn't wired for plugins) — never executed unsupervised.
func TestAPIPluginCallPolicyGated(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	b := bus.New(100)
	m.SetBus(b)
	v := vault.New(filepath.Join(t.TempDir(), "v.age"))
	m.SetPolicy(policy.NewEngine(map[string]policy.Policy{
		"p": {Deny: []string{"plugin danger"}, Confirm: []string{"plugin risky"}},
	}), map[string]string{"agent": "p"})
	pl := plugin.New(t.TempDir())
	mux := New(m, b, nil, fleet.New(nil, okRunner{}, 2), v, pl, "test").Mux()

	if code, out := do(t, mux, "POST", "/api/plugin/call", `{"owner":"agent","name":"danger"}`); code != 422 {
		t.Fatalf("denied plugin => %d %v, want 422", code, out)
	}
	if code, out := do(t, mux, "POST", "/api/plugin/call", `{"owner":"agent","name":"risky"}`); code != 422 {
		t.Fatalf("confirm-required plugin => %d %v, want 422 (fail closed)", code, out)
	}
}

func TestAPIPluginErrorsAreRedacted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo")
	body := `#!/bin/sh
if [ "$1" = describe ]; then
  echo '{"tools":[{"name":"fail","description":"fails","inputSchema":{"type":"object"}}]}'
else
  echo 'plugin-secret' >&2
  exit 1
fi
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	pl := plugin.New(dir)
	if err := pl.Load(); err != nil {
		t.Fatal(err)
	}
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	if err := m.Redactor().AddLiteral("plugin-secret"); err != nil {
		t.Fatal(err)
	}
	mux := New(m, nil, nil, nil, nil, pl, "test").Mux()
	_, out := do(t, mux, http.MethodPost, "/api/plugin/call", `{"owner":"agent","name":"demo.fail","args":{}}`)
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "plugin-secret") {
		t.Fatalf("plugin error leaked secret: %s", encoded)
	}
}

// The (local) default session must still read/write the daemon host over HTTP —
// the new session-aware guard only refuses REMOTE sessions, not local ones.
func TestAPIFileReadWriteLocalSession(t *testing.T) {
	mux, _ := newTestServer(t)
	path := filepath.Join(t.TempDir(), "note.txt")

	wbody, _ := json.Marshal(map[string]any{"owner": "agent", "path": path, "content": "hello-local"})
	if code, out := do(t, mux, "POST", "/api/file/write", string(wbody)); code != 200 {
		t.Fatalf("file/write => %d %v", code, out)
	}
	rbody, _ := json.Marshal(map[string]any{"owner": "agent", "path": path})
	code, out := do(t, mux, "POST", "/api/file/read", string(rbody))
	if code != 200 {
		t.Fatalf("file/read => %d %v", code, out)
	}
	if c, _ := out["content"].(string); c != "hello-local" {
		t.Fatalf("content = %q, want hello-local", c)
	}
}
