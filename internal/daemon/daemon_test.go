package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

const testCLIToken = "cli-secret-token-1234567890abcdef"
const testDashToken = "dash-secret-token-abcdef1234567890"

// In local-trust mode the read/observe API answers without a token, but the
// security-sensitive mutating routes must STILL require it — otherwise an agent
// on the same loopback/uid could curl the TCP dashboard and self-approve or
// rewrite its policy, bypassing the socket guard.
func TestTokenAuthGatesSensitiveRoutesInLocalTrust(t *testing.T) {
	var reached []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	h := tokenAuth(testDashToken, true /*localTrust*/, inner)

	req := func(path string, withToken bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "http://127.0.0.1:7717"+path, nil)
		if withToken {
			r.Header.Set("Authorization", "Bearer "+testDashToken)
		}
		h.ServeHTTP(rec, r)
		return rec
	}

	sensitive := []string{"/api/approve", "/api/deny", "/api/stop_all", "/api/policies/set", "/api/policies/remove", "/api/servers/add", "/api/servers/remove"}

	// Tokenless sensitive calls (an agent's curl) are refused even in local-trust.
	for _, p := range sensitive {
		if code := req(p, false).Code; code != http.StatusUnauthorized {
			t.Fatalf("%s tokenless in local-trust => %d, want 401", p, code)
		}
	}
	if len(reached) != 0 {
		t.Fatalf("sensitive routes leaked to inner handler without a token: %v", reached)
	}

	// With the dashboard token (the SPA), they pass through.
	for _, p := range sensitive {
		if code := req(p, true).Code; code != http.StatusOK {
			t.Fatalf("%s with token in local-trust => %d, want 200", p, code)
		}
	}

	// Read/observe routes still answer tokenless in local-trust (the dashboard
	// loads without a token).
	for _, p := range []string{"/api/status", "/api/pending", "/api/exec/list"} {
		if code := req(p, false).Code; code != http.StatusOK {
			t.Fatalf("%s tokenless in local-trust => %d, want 200 (read is open)", p, code)
		}
	}
}

// The human-only mutating routes must be refused over the UDS (which the agent's
// shim — and a shelled-out curl — can reach), and served only over the TCP
// dashboard. Everything else (that isn't a CLI-token-gated route) passes through.
func TestDashboardOnlyGatesMutatingRoutesOnUDS(t *testing.T) {
	var reached []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	h := udsGuard(testCLIToken, inner)

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

	// Plain agent + CLI routes pass through untouched.
	for _, p := range []string{"/api/exec/run", "/api/vault/unlock", "/api/policies", "/api/status"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s over UDS => %d, want 200 (pass-through)", p, rec.Code)
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
	h := udsGuard(testCLIToken, inner)

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
	h := udsGuard("", inner)

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
	// root resolves (uid 0) — every system has it; used to check uid:gid path
	if uid, _, err := resolveRunAs("0:0"); err != nil || uid != 0 {
		t.Fatalf(`resolveRunAs("0:0") = %d,_,%v; want 0,_,nil`, uid, err)
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
		// Running as root: a valid unprivileged spec enables separation, but a
		// spec resolving to root is refused.
		if sp, err := resolveSpawn("1234:5678"); err != nil || !sp.SeparateUID || sp.UID != 1234 {
			t.Fatalf("resolveSpawn(unprivileged) as root = %+v,%v; want enabled", sp, err)
		}
		if _, err := resolveSpawn("0:0"); err == nil {
			t.Fatal("resolveSpawn resolving to uid 0 should be refused")
		}
	}
}
