package controlplane

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPingFallsBackToLegacyStatusOnNotFound(t *testing.T) {
	var statusCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ping":
			http.NotFound(w, r)
		case "/api/status":
			statusCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"legacy"}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), base: srv.URL}
	if err := c.Ping(); err != nil {
		t.Fatalf("legacy ping fallback: %v", err)
	}
	if got := statusCalls.Load(); got != 1 {
		t.Fatalf("legacy status calls = %d, want 1", got)
	}
}

func TestPingDoesNotFallbackForNon404Response(t *testing.T) {
	var statusCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			statusCalls.Add(1)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), base: srv.URL}
	if err := c.Ping(); err == nil {
		t.Fatal("unauthorized ping unexpectedly succeeded")
	}
	if got := statusCalls.Load(); got != 0 {
		t.Fatalf("legacy status calls = %d, want 0", got)
	}
}
