package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// initialize must hand every connecting client the server instructions, so an
// agent without the termada skill still learns to default to termada and how the
// human-approval flow works.
func TestInitializeReturnsInstructions(t *testing.T) {
	s := newTestServer(t)
	res, rerr := s.dispatch(rpcRequest{Method: "initialize", Params: json.RawMessage(`{"clientInfo":{"name":"x"}}`)})
	if rerr != nil {
		t.Fatalf("initialize errored: %v", rerr)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T", res)
	}
	instr, ok := m["instructions"].(string)
	if !ok || instr == "" {
		t.Fatalf("instructions missing/empty: %v", m["instructions"])
	}
	for _, want := range []string{"exec_run", "ssh", "awaiting_confirmation", "self-approve", "capabilities()"} {
		if !strings.Contains(instr, want) {
			t.Fatalf("instructions missing %q", want)
		}
	}
}
