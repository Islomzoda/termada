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
