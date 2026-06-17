package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The human-only mutating routes must be refused over the UDS (which the agent's
// shim — and a shelled-out curl — can reach), and served only over the TCP
// dashboard. Everything else passes through.
func TestDashboardOnlyGatesMutatingRoutesOnUDS(t *testing.T) {
	var reached []string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	h := dashboardOnly(inner)

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

	// Agent + CLI routes pass through untouched.
	for _, p := range []string{"/api/exec/run", "/api/vault/unlock", "/api/stop_all", "/api/policies", "/api/status"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s over UDS => %d, want 200 (pass-through)", p, rec.Code)
		}
	}
}
