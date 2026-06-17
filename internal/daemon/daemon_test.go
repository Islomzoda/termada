package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testCLIToken = "cli-secret-token-1234567890abcdef"

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
