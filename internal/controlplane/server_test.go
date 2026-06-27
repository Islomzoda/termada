package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			_, p := do(t, mux, "POST", "/api/exec/poll", `{"job_id":"`+jid+`"}`)
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
	_, st := do(t, mux, "GET", "/api/status", "")
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
}

func TestAPIServerAddRequiresVaultThenWorks(t *testing.T) {
	mux, _ := newTestServer(t)
	// adding with a secret before unlocking the vault must fail
	if code, out := do(t, mux, "POST", "/api/servers/add", `{"name":"web1","host":"10.0.0.1","user":"deploy","secret":"pw"}`); code == 200 {
		t.Fatalf("expected failure before vault unlock, got %v", out)
	}
	// unlock (creates the vault on first use)
	if code, _ := do(t, mux, "POST", "/api/vault/unlock", `{"passphrase":"pw"}`); code != 200 {
		t.Fatalf("unlock failed")
	}
	if code, out := do(t, mux, "POST", "/api/servers/add", `{"name":"web1","host":"10.0.0.1","user":"deploy","secret":"pw","tags":["web"]}`); code != 200 {
		t.Fatalf("add failed: %v", out)
	}
	_, st := do(t, mux, "GET", "/api/status", "")
	servers, _ := st["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("server not listed: %v", servers)
	}
	// connectivity test (mock runner returns ok)
	code, out := do(t, mux, "POST", "/api/servers/test", `{"name":"web1"}`)
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
	_, st := do(t, mux, "GET", "/api/status", "")
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

func TestAPISessionLifecycle(t *testing.T) {
	mux, _ := newTestServer(t)
	code, out := do(t, mux, "POST", "/api/session/create", `{"owner":"t"}`)
	if code != 200 || out["session_id"] == nil {
		t.Fatalf("create session = %d %v", code, out)
	}
	sid := out["session_id"].(string)
	_, st := do(t, mux, "GET", "/api/status", "")
	if sessions, _ := st["sessions"].([]any); len(sessions) != 1 {
		t.Fatalf("session not in status: %v", st["sessions"])
	}
	if code, _ := do(t, mux, "POST", "/api/session/close", `{"session_id":"`+sid+`"}`); code != 200 {
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
	if code, out := do(t, mux, "POST", "/api/policies/set", `{"name":"ci","allow":["ls"],"deny":["sudo*"]}`); code != 200 {
		t.Fatalf("set ci => %d %v", code, out)
	}
	// It reports as managed (editable); the config policy does not.
	_, pol := do(t, mux, "GET", "/api/policies", "")
	managed, _ := pol["managed"].(map[string]any)
	if managed["ci"] != true || managed["prod"] == true {
		t.Fatalf("managed flags wrong: %v", pol["managed"])
	}

	// A config-defined policy is read-only via the API.
	if code, _ := do(t, mux, "POST", "/api/policies/set", `{"name":"prod","allow":["*"]}`); code == 200 {
		t.Fatal("editing a config-defined policy should be refused")
	}
	// Removing a policy still assigned to an agent is refused (no silent allow-all).
	if code, _ := do(t, mux, "POST", "/api/policies/remove", `{"name":"ci"}`); code == 200 {
		t.Fatal("removing an assigned policy should be refused")
	}
	// Reassign the agent, then ci can be removed.
	m.SetPolicy(m.Policy(), map[string]string{"agentX": "prod"})
	if code, out := do(t, mux, "POST", "/api/policies/remove", `{"name":"ci"}`); code != 200 {
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
	if code, _ := do(t, mux, "POST", "/api/policies/set", `{"name":"aa","confirm":["deploy*","release*"]}`); code != 200 {
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

// The (local) default session must still read/write the daemon host over HTTP —
// the new session-aware guard only refuses REMOTE sessions, not local ones.
func TestAPIFileReadWriteLocalSession(t *testing.T) {
	mux, _ := newTestServer(t)
	path := filepath.Join(t.TempDir(), "note.txt")

	wbody, _ := json.Marshal(map[string]any{"path": path, "content": "hello-local"})
	if code, out := do(t, mux, "POST", "/api/file/write", string(wbody)); code != 200 {
		t.Fatalf("file/write => %d %v", code, out)
	}
	rbody, _ := json.Marshal(map[string]any{"path": path})
	code, out := do(t, mux, "POST", "/api/file/read", string(rbody))
	if code != 200 {
		t.Fatalf("file/read => %d %v", code, out)
	}
	if c, _ := out["content"].(string); c != "hello-local" {
		t.Fatalf("content = %q, want hello-local", c)
	}
}
