package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/fleet"
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
	code, out := do(t, mux, "POST", "/api/session/create", `{"owner":"t"}`)
	if code != 200 || out["session_id"] == nil {
		t.Fatalf("create session = %d %v", code, out)
	}
	sid := out["session_id"].(string)
	_, st := doOperator(t, mux, "GET", "/api/status", "")
	if sessions, _ := st["sessions"].([]any); len(sessions) != 1 {
		t.Fatalf("session not in status: %v", st["sessions"])
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
