package controlplane

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/termada/termada/internal/bus"
	"github.com/termada/termada/internal/engine"
)

func TestExecListRecentLimitCompactsBeforeSerialization(t *testing.T) {
	mux, m := newTestServer(t)
	base := time.Now().UnixMilli()
	const omittedMarker = "OMITTED-LARGE-RECORD-MARKER"
	const includedTail = "INCLUDED-COMMAND-TAIL-MUST-BE-TRUNCATED"
	stored := []engine.Info{
		{
			JobID: "job_old", Owner: "agent", Status: engine.StatusExited,
			CreatedUnixMS: base - 1, Command: []string{"echo", strings.Repeat("o", 64<<10) + omittedMarker},
			StreamAvailable: true,
		},
		{
			JobID: "job_a", Owner: "agent", Status: engine.StatusExited,
			CreatedUnixMS: base, Command: []string{"echo", strings.Repeat("a", 64<<10) + omittedMarker},
			StreamAvailable: true,
		},
		{
			JobID: "job_z", Owner: "agent", Status: engine.StatusExited,
			CreatedUnixMS: base, Command: []string{"echo", strings.Repeat("z", 64<<10) + includedTail},
			StreamAvailable: true,
		},
	}
	path := filepath.Join(t.TempDir(), "registry.json")
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	if err := m.EnablePersistence(path); err != nil {
		t.Fatalf("enable persistence: %v", err)
	}

	req := WithOperatorPrincipal(httptest.NewRequest(http.MethodGet, "/api/exec/list?filter=recent&limit=1&summary=1", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recent list status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), omittedMarker) {
		t.Fatalf("omitted large record was serialized: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), includedTail) {
		t.Fatalf("included command tail was not compacted: %s", rec.Body.String())
	}
	if rec.Body.Len() > 4<<10 {
		t.Fatalf("compact response is %d bytes, want <= 4096", rec.Body.Len())
	}
	var response struct {
		Jobs    []engine.Info `json:"jobs"`
		Limit   int           `json:"limit"`
		Omitted int           `json:"omitted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Jobs) != 1 || response.Jobs[0].JobID != "job_z" {
		t.Fatalf("recent jobs = %+v, want deterministic newest job_z", response.Jobs)
	}
	if response.Limit != 1 || response.Omitted != 2 {
		t.Fatalf("recent pagination = limit %d omitted %d", response.Limit, response.Omitted)
	}
	if response.Jobs[0].StreamAvailable {
		t.Fatalf("recovered job advertised a stream: %+v", response.Jobs[0])
	}

	code, _ := doOperator(t, mux, http.MethodGet, "/api/exec/list?filter=recent&limit=201&summary=1", "")
	if code == http.StatusOK {
		t.Fatal("recent list accepted a limit above the hard maximum")
	}

	// Existing callers that do not opt into a limit retain the full response.
	fullReq := WithOperatorPrincipal(httptest.NewRequest(http.MethodGet, "/api/exec/list?filter=recent", nil))
	fullRec := httptest.NewRecorder()
	mux.ServeHTTP(fullRec, fullReq)
	if fullRec.Code != http.StatusOK {
		t.Fatalf("unbounded recent status = %d body=%s", fullRec.Code, fullRec.Body.String())
	}
	if !strings.Contains(fullRec.Body.String(), omittedMarker) {
		t.Fatal("unbounded legacy response unexpectedly compacted or omitted jobs")
	}
}

func TestDashboardStateIsBoundedCompactAndRevisioned(t *testing.T) {
	mux, m := newTestServer(t)
	base := time.Now().UnixMilli()
	stored := make([]engine.Info, 0, 4)
	for i := 0; i < 4; i++ {
		stored = append(stored, engine.Info{
			JobID: fmt.Sprintf("job_%d", i), Owner: "agent", Workspace: "termada",
			Status: engine.StatusExited, CreatedUnixMS: base + int64(i),
			Command: []string{"printf", strings.Repeat("x", 8<<10)},
		})
	}
	path := filepath.Join(t.TempDir(), "registry.json")
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	if err := m.Bus().Publish(bus.Event{Type: bus.EvAgentConnected, AgentID: "agent"}); err != nil {
		t.Fatal(err)
	}

	req := WithOperatorPrincipal(httptest.NewRequest(http.MethodGet, "/api/dashboard/state?limit=2", nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard state status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Jobs          []engine.Info `json:"jobs"`
		JobsOmitted   int           `json:"jobs_omitted"`
		StateRevision uint64        `json:"state_revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Jobs) != 2 || response.JobsOmitted != 2 || response.StateRevision == 0 {
		t.Fatalf("dashboard state = jobs:%d omitted:%d revision:%d", len(response.Jobs), response.JobsOmitted, response.StateRevision)
	}
	if rec.Body.Len() > 8<<10 || response.Jobs[0].Workspace != "termada" || len(response.Jobs[0].Command[1]) > maxCommandSummaryBytes {
		t.Fatalf("dashboard state is not compact: bytes=%d first=%+v", rec.Body.Len(), response.Jobs[0])
	}
}

func TestEventStreamReplaysAfterCursorWithSSEIDs(t *testing.T) {
	mux, m := newTestServer(t)
	if err := m.Bus().Publish(bus.Event{Type: bus.EvJobStarted, JobID: "job_1"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Bus().Publish(bus.Event{Type: bus.EvJobFinished, JobID: "job_2"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := WithOperatorPrincipal(httptest.NewRequest(http.MethodGet, "/api/events?since=1", nil).WithContext(ctx))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(rec, req)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event stream did not stop after cancellation")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "id: 2\n") || strings.Contains(body, "id: 1\n") || !strings.Contains(body, `"job_id":"job_2"`) {
		t.Fatalf("cursor replay = %q", body)
	}
}

func TestCompactCommandSummaryPreservesUTF8(t *testing.T) {
	command := []string{"printf", strings.Repeat("я", maxCommandSummaryBytes)}
	compacted := compactCommandSummary(command)
	if len(compacted) != 2 || !utf8.ValidString(compacted[1]) {
		t.Fatalf("compact command is not valid UTF-8: %#v", compacted)
	}
	if len(strings.Join(compacted, " ")) > maxCommandSummaryBytes {
		t.Fatalf("compact command exceeded summary bound: %d bytes", len(strings.Join(compacted, " ")))
	}

	exact := compactCommandSummary([]string{strings.Repeat("x", maxCommandSummaryBytes), "omitted"})
	if got := len(strings.Join(exact, " ")); got > maxCommandSummaryBytes {
		t.Fatalf("exact-boundary command summary = %d bytes, want <= %d", got, maxCommandSummaryBytes)
	}
}

func TestSessionStreamDrainsAfterClose(t *testing.T) {
	m := engine.NewManager(engine.DefaultConfig())
	t.Cleanup(m.Shutdown)
	sess, err := m.CreateSession("agent", "local", "shell")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	job, err := m.Start("agent", sess.ID, []string{"bash", "-c", "printf 'visible\\nfinal-tail'; sleep 30"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start job: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		polled, pollErr := m.PollFor("", job.ID, "", true)
		if pollErr != nil {
			t.Fatalf("poll job: %v", pollErr)
		}
		if strings.Contains(polled.StdoutChunk, "visible") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	server := New(m, nil, nil, nil, nil, nil, "test")
	mux := server.Mux()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, WithOperatorPrincipal(r))
	}))
	defer ts.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ts.URL + "/api/session/stream?session_id=" + sess.ID)
	if err != nil {
		t.Fatalf("open session stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	first := readTestSSEData(t, scanner)
	output, _ := first["chunk"].(string)
	if err := m.CloseSession("agent", sess.ID); err != nil {
		t.Fatalf("close session: %v", err)
	}

	done := false
	for i := 0; i < 5 && !done; i++ {
		event := readTestSSEData(t, scanner)
		if event["error"] != nil {
			t.Fatalf("normal session close emitted stream error: %v", event)
		}
		if chunk, _ := event["chunk"].(string); chunk != "" {
			output += chunk
		}
		done, _ = event["done"].(bool)
	}
	if !done {
		t.Fatal("session stream did not emit done after close")
	}
	if !strings.Contains(output, "final-tail") {
		t.Fatalf("session stream lost final buffered output: %q", output)
	}
}

func TestExecStreamDrainsAfterJobRegistryCleanup(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.MaxOutputBytes = 4
	m := engine.NewManager(cfg)
	t.Cleanup(m.Shutdown)
	target, err := m.Start("agent", "", []string{"printf", "0123456789"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start target job: %v", err)
	}
	select {
	case <-target.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("target job did not finish")
	}
	time.Sleep(5 * time.Millisecond)
	newer, err := m.Start("agent", "", []string{"true"}, engine.ModeForeground)
	if err != nil {
		t.Fatalf("start newer job: %v", err)
	}
	select {
	case <-newer.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("newer job did not finish")
	}

	server := New(m, nil, nil, nil, nil, nil, "test")
	mux := server.Mux()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, WithOperatorPrincipal(r))
	}))
	defer ts.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ts.URL + "/api/exec/stream?job_id=" + target.ID)
	if err != nil {
		t.Fatalf("open job stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	first := readTestSSEData(t, scanner)
	outputText, _ := first["chunk"].(string)

	m.GCOnce(0, 1)
	if _, err := m.PollFor("agent", target.ID, "", true); err == nil {
		t.Fatal("registry poll found target after cleanup")
	}
	done := false
	for page := 0; page < 8 && !done; page++ {
		event := readTestSSEData(t, scanner)
		if event["error"] != nil {
			t.Fatalf("stable job stream emitted error: %v", event)
		}
		if chunk, _ := event["chunk"].(string); chunk != "" {
			outputText += chunk
		}
		done, _ = event["done"].(bool)
	}
	if !done {
		t.Fatal("stable job stream did not emit done")
	}
	if !strings.Contains(outputText, "0123456789") {
		t.Fatalf("stable job stream output = %q, want complete output", outputText)
	}
}

func readTestSSEData(t *testing.T, scanner *bufio.Scanner) map[string]any {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("decode SSE event: %v", err)
		}
		return event
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE stream: %v", err)
	}
	t.Fatal("SSE stream ended without another data event")
	return nil
}
