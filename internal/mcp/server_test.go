package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"
)

func TestReadLineBoundsAndRecoversAfterOversizedMessage(t *testing.T) {
	next := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	input := strings.Repeat("x", maxMCPMessageBytes+1) + "\n" + next + "\n"
	reader := bufio.NewReaderSize(strings.NewReader(input), 1024)

	if line, err := readLine(reader); err != errMCPMessageTooLarge || line != nil {
		t.Fatalf("oversized readLine = (%d bytes, %v)", len(line), err)
	}
	line, err := readLine(reader)
	if err != nil || string(line) != next {
		t.Fatalf("next message after oversized line = (%q, %v)", line, err)
	}
}

func TestWriteBoundsOversizedResponse(t *testing.T) {
	var out bytes.Buffer
	s := &Server{enc: json.NewEncoder(&out), logger: log.New(io.Discard, "", 0)}
	s.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`7`), Result: strings.Repeat("x", maxMCPResponseBytes)})
	if out.Len() > maxMCPResponseBytes {
		t.Fatalf("bounded response wrote %d bytes", out.Len())
	}
	var response rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != -32603 || string(response.ID) != "7" {
		t.Fatalf("oversized response fallback = %+v", response)
	}
}

func TestMCPAgentIDValidation(t *testing.T) {
	for _, good := range []string{"claude-code", "agent_1"} {
		if !validMCPAgentID(good) {
			t.Errorf("valid id %q rejected", good)
		}
	}
	for _, bad := range []string{"", "has space", strings.Repeat("x", 129), "line\nbreak"} {
		if validMCPAgentID(bad) {
			t.Errorf("invalid id %q accepted", bad)
		}
	}
}
