package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/termada/termada/internal/audit"
	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/controlplane"
	"github.com/termada/termada/internal/engine"
)

const testCLIToken = "cli-secret-token-1234567890abcdef"
const testDashToken = "dash-secret-token-abcdef1234567890"

func udsGuardForTest(t *testing.T, cliToken string, h http.Handler) http.Handler {
	t.Helper()
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	return udsGuard(cliToken, m, h)
}

// Every TCP API route requires the dashboard token. Host is retained only as a
// DNS-rebinding sanity check and never grants trust by itself.
func TestTokenAuthRequiresTokenForEntireAPI(t *testing.T) {
	var reached []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	h := tokenAuth(testDashToken, inner)

	req := func(path string, withToken bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://127.0.0.1:7717"+path, nil)
		r.RemoteAddr = "203.0.113.10:4242"
		if withToken {
			r.Header.Set("Authorization", "Bearer "+testDashToken)
		}
		h.ServeHTTP(rec, r)
		return rec
	}

	sensitive := []string{"/api/approve", "/api/exec/run", "/api/status", "/api/session/list", "/api/file/read", "/api/events", "/metrics"}

	// A remote caller can spoof Host: localhost, but without the token it still
	// cannot reach even ordinary exec/file routes.
	for _, p := range sensitive {
		if code := req(p, false).Code; code != http.StatusUnauthorized {
			t.Fatalf("%s tokenless => %d, want 401", p, code)
		}
	}
	if len(reached) != 0 {
		t.Fatalf("sensitive routes leaked to inner handler without a token: %v", reached)
	}

	// With the dashboard token, they pass through.
	for _, p := range sensitive {
		if code := req(p, true).Code; code != http.StatusOK {
			t.Fatalf("%s with token => %d, want 200", p, code)
		}
	}

	// Static dashboard assets remain loadable so the UI can present its token gate.
	if code := req("/", false).Code; code != http.StatusOK {
		t.Fatalf("static dashboard tokenless => %d, want 200", code)
	}
}

func TestDaemonRootRoutesMetricsToControlPlane(t *testing.T) {
	cp := http.NewServeMux()
	cp.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("termada_jobs_total 1\n"))
	})
	dashboard := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "dashboard", http.StatusNotFound)
	})
	rec := httptest.NewRecorder()
	daemonRootHandler(cp, dashboard).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "termada_jobs_total") {
		t.Fatalf("metrics routing = %d %q", rec.Code, rec.Body.String())
	}
}

func TestShutdownAuditsTerminalJobsBeforeCancellingReliableSink(t *testing.T) {
	mgr := engine.NewManager(engine.DefaultConfig())
	events := bus.New(32)
	mgr.SetBus(events)
	logger, err := audit.Open(filepath.Join(t.TempDir(), "audit.log"), mgr.Redactor())
	if err != nil {
		t.Fatal(err)
	}
	cancelAudit := events.SubscribeReliable(logger.FromBus)
	d := &Daemon{mgr: mgr, bus: events, audit: logger}

	job, err := mgr.Start("agent", "", []string{"sleep", "30"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start job: %v", err)
	}
	d.shutdownServices(cancelAudit)
	select {
	case <-job.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish the active job")
	}

	records, err := logger.Tail(32)
	if err != nil {
		t.Fatal(err)
	}
	started, finished := -1, -1
	for i, record := range records {
		if record.JobID != job.ID {
			continue
		}
		switch record.Type {
		case bus.EvJobStarted:
			started = i
		case bus.EvJobFinished:
			finished = i
		}
	}
	if started < 0 || finished <= started {
		t.Fatalf("job audit order started=%d finished=%d records=%+v", started, finished, records)
	}
}

func TestTokenizedDashboardURLUsesSafeLocalHost(t *testing.T) {
	if base := dashboardBaseURL(&net.TCPAddr{IP: net.IPv4zero, Port: 9876}); base != "http://127.0.0.1:9876/" {
		t.Fatalf("base URL = %q", base)
	}
	u := tokenizedDashboardURL(&net.TCPAddr{IP: net.IPv4zero, Port: 7717}, "a token")
	if u != "http://127.0.0.1:7717/?token=a+token" {
		t.Fatalf("url = %q", u)
	}
	if !strings.Contains(tokenizedDashboardURL(&net.TCPAddr{IP: net.ParseIP("::1"), Port: 7717}, testDashToken), "[::1]:7717") {
		t.Fatal("IPv6 dashboard URL is not correctly bracketed")
	}
}

func TestDaemonLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	unlock, err := acquireDaemonLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireDaemonLock(path); err == nil {
		second()
		t.Fatal("second daemon lock was acquired concurrently")
	}
	unlock()
	third, err := acquireDaemonLock(path)
	if err != nil {
		t.Fatalf("lock remained held after release: %v", err)
	}
	third()
}

func TestLoadOrCreateTokenIsStrictAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	token, err := loadOrCreateTokenAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 {
		t.Fatalf("generated token length = %d, want 64", len(token))
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("token mode = %o, want 600", got)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := loadOrCreateTokenAt(path)
	if err != nil || again != token {
		t.Fatalf("reload token = %q, %v", again, err)
	}
	fi, _ = os.Stat(path)
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("repaired token mode = %o, want 600", got)
	}
}

func TestLoadOrCreateTokenRejectsCorruptOrSymlinkFile(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt")
	if err := os.WriteFile(corrupt, []byte("                \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateTokenAt(corrupt); err == nil {
		t.Fatal("whitespace-only token file was accepted")
	}

	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(testDashToken), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked-token")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := loadOrCreateTokenAt(link); err == nil {
		t.Fatal("symlink token file was accepted")
	}

	oversized := filepath.Join(dir, "oversized-token")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("a", 4097)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateTokenAt(oversized); err == nil {
		t.Fatal("oversized token file was accepted")
	}
}

func TestAnonymousUDSPingKeepsShimConnectivity(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "td-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "t.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: udsGuardForTest(t, testCLIToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ping" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })

	client := controlplane.NewUnixClient(sock)
	if err := client.Ping(); err != nil {
		t.Fatalf("tokenless shim ping over UDS failed: %v", err)
	}
}

func TestTransportAuthInjectsOperatorPrincipal(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	cp := controlplane.New(m, nil, nil, nil, nil, nil, "test").Mux()

	uds := udsGuard(testCLIToken, m, cp)
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("X-Termada-CLI-Token", testCLIToken)
	rec := httptest.NewRecorder()
	uds.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated UDS operator status = %d: %s", rec.Code, rec.Body.String())
	}

	tcp := tokenAuth(testDashToken, cp)
	req = httptest.NewRequest("GET", "http://127.0.0.1:7717/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+testDashToken)
	rec = httptest.NewRecorder()
	tcp.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated TCP operator status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUDSLegacyStatusProbeReturnsOnlyHealth(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetAgentTokens(map[string]string{"known-token-123456": "known-agent"})
	m.RecordConnect("sensitive-agent")
	cp := controlplane.New(m, nil, nil, nil, nil, nil, "sensitive-version").Mux()
	h := udsGuard(testCLIToken, m, cp)

	assertHealthOnly := func(name, agentToken string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		if agentToken != "" {
			req.Header.Set("X-Termada-Agent-Token", agentToken)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s legacy status probe = %d: %s", name, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s legacy status JSON: %v", name, err)
		}
		if len(body) != 1 || body["ok"] != true {
			t.Fatalf("%s legacy status leaked operator data: %v", name, body)
		}
	}
	assertHealthOnly("anonymous", "")
	assertHealthOnly("agent", "known-token-123456")

	unknown := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	unknown.Header.Set("X-Termada-Agent-Token", "unknown-token-123456")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, unknown)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unknown agent token status probe = %d, want 403", rec.Code)
	}

	operator := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	operator.Header.Set("X-Termada-CLI-Token", testCLIToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, operator)
	var full map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &full) != nil {
		t.Fatalf("operator status = %d: %s", rec.Code, rec.Body.String())
	}
	if full["version"] != "sensitive-version" || full["jobs"] == nil || full["agents"] == nil {
		t.Fatalf("operator did not receive full status: %v", full)
	}

	post := httptest.NewRequest(http.MethodPost, "/api/status", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, post)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tokenless POST status = %d, want 403", rec.Code)
	}
}

func TestUDSRejectsUnknownAgentTokenBeforeDiscoveryRoutes(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	m.SetAgentTokens(map[string]string{"known-token-123456": "agent"})
	reached := false
	h := udsGuard(testCLIToken, m, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	unknown := httptest.NewRequest(http.MethodGet, "/api/plugin/tools", nil)
	unknown.Header.Set("X-Termada-Agent-Token", "unknown-token-123456")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, unknown)
	if rec.Code != http.StatusForbidden || reached {
		t.Fatalf("unknown token discovery = %d reached=%v, want transport rejection", rec.Code, reached)
	}

	known := httptest.NewRequest(http.MethodGet, "/api/plugin/tools", nil)
	known.Header.Set("X-Termada-Agent-Token", "known-token-123456")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, known)
	if rec.Code != http.StatusOK || !reached {
		t.Fatalf("known token discovery = %d reached=%v", rec.Code, reached)
	}
}

// Operator routes on UDS require the CLI token; tokenless agent routes and the
// liveness probe remain available to the shim.
func TestUDSGatesOperatorRoutes(t *testing.T) {
	var reached []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	h := udsGuardForTest(t, testCLIToken, inner)

	for _, p := range []string{"/api/policies/set", "/api/policies/remove", "/api/servers/add", "/api/servers/remove"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", p, nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s over UDS => %d, want 403", p, rec.Code)
		}
	}
	if len(reached) != 0 {
		t.Fatalf("blocked routes leaked to the inner handler: %v", reached)
	}

	// Plain agent routes and the data-free liveness probe pass through untouched.
	for _, p := range []string{"/api/exec/run", "/api/ping"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s over UDS => %d, want 200 (pass-through)", p, rec.Code)
		}
	}

	// The same operator routes pass when the actual CLI credential is present.
	for _, p := range []string{"/api/policies/set", "/api/servers/add", "/api/status"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", p, nil)
		req.Header.Set("X-Termada-CLI-Token", testCLIToken)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s over UDS with CLI token => %d, want 200", p, rec.Code)
		}
	}
}

// The human approval routes stay reachable over the UDS (the CLI needs them) but
// only with the CLI auth token. A tokenless or wrong-token request — what an
// agent shelling out to curl the socket can produce — is refused, so an agent
// cannot self-approve, self-deny, or stop-all over the socket.
func TestUDSRequiresCLITokenForApprovalRoutes(t *testing.T) {
	var reached []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	h := udsGuardForTest(t, testCLIToken, inner)

	routes := []string{"/api/approve", "/api/deny", "/api/stop_all"}

	// No token (the agent's blind curl): refused.
	for _, p := range routes {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", p, nil))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s over UDS without CLI token => %d, want 403", p, rec.Code)
		}
	}

	// Wrong token: refused.
	for _, p := range routes {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", p, nil)
		req.Header.Set("X-Termada-CLI-Token", "not-the-token")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s over UDS with wrong CLI token => %d, want 403", p, rec.Code)
		}
	}

	if len(reached) != 0 {
		t.Fatalf("approval routes leaked to the inner handler without a valid token: %v", reached)
	}

	// Correct token (the human CLI): passes through.
	for _, p := range routes {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", p, nil)
		req.Header.Set("X-Termada-CLI-Token", testCLIToken)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s over UDS with valid CLI token => %d, want 200 (pass-through)", p, rec.Code)
		}
	}
	if len(reached) != len(routes) {
		t.Fatalf("valid-token requests reached inner=%v, want all %v", reached, routes)
	}
}

// Fail closed: if the daemon somehow has no CLI token, the approval routes must
// be refused rather than silently accepting an empty token == empty header.
func TestUDSApprovalRefusedWhenNoCLIToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := udsGuardForTest(t, "", inner)

	for _, p := range []string{"/api/approve", "/api/deny", "/api/stop_all"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", p, nil)
		req.Header.Set("X-Termada-CLI-Token", "") // empty header, empty server token
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s with empty server token => %d, want 403 (fail closed)", p, rec.Code)
		}
	}
}

// resolveRunAs parses usernames and numeric specs; resolveSpawn enforces the
// fail-closed rules (needs root, must not resolve to root).
func TestResolveRunAs(t *testing.T) {
	// explicit numeric uid:gid needs no user database
	if uid, gid, err := resolveRunAs("1234:5678"); err != nil || uid != 1234 || gid != 5678 {
		t.Fatalf(`resolveRunAs("1234:5678") = %d,%d,%v; want 1234,5678,nil`, uid, gid, err)
	}
	// bare numeric id with a bad pair
	if _, _, err := resolveRunAs("12:bad"); err == nil {
		t.Fatal(`resolveRunAs("12:bad") should error`)
	}
	for _, invalid := range []string{
		"0:1", "1:0", "-1:1000", "1000:-1",
		"4294967295:1000", "1000:4294967295",
		"4294967296:1000", "1000:4294967296",
	} {
		if _, _, err := resolveRunAs(invalid); err == nil {
			t.Fatalf("resolveRunAs(%q) should reject root/overflow credentials", invalid)
		}
	}
	// a username that cannot exist
	if _, _, err := resolveRunAs("no-such-user-xyz-123"); err == nil {
		t.Fatal("resolveRunAs(unknown user) should error")
	}
}

func TestResolveSpawnFailClosed(t *testing.T) {
	// Empty spec = no separation, always OK regardless of privilege.
	if sp, err := resolveSpawn(""); err != nil || sp.SeparateUID {
		t.Fatalf(`resolveSpawn("") = %+v,%v; want disabled,nil`, sp, err)
	}

	// A non-empty spec while not root must fail closed (we can't setuid).
	if os.Geteuid() != 0 {
		if _, err := resolveSpawn("1234:5678"); err == nil {
			t.Fatal("resolveSpawn with run_as set but not root should fail closed")
		}
	} else {
		// Running as root: a valid unprivileged spec enables separation, while
		// root/overflow identities have already failed in resolveRunAs.
		if sp, err := resolveSpawn("1234:5678"); err != nil || !sp.SeparateUID || sp.UID != 1234 {
			t.Fatalf("resolveSpawn(unprivileged) as root = %+v,%v; want enabled", sp, err)
		}
		if _, err := resolveSpawn("0:1"); err == nil {
			t.Fatal("resolveSpawn resolving to uid 0 should be refused")
		}
	}
}
