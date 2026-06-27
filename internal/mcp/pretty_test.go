package mcp

import (
	"strings"
	"testing"

	"github.com/termada/termada/internal/engine"
	"github.com/termada/termada/internal/errs"
)

func TestPrettyResultExited(t *testing.T) {
	out := prettyResult(map[string]any{
		"status":     engine.StatusExited,
		"exit_code":  0,
		"stdout":     "compiled ok\n",
		"session_id": "s_3f9a",
	}, false)
	for _, want := range []string{"✅", "exited", "exit 0", "session s_3f9a", "compiled ok"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPrettyResultNonzeroExit(t *testing.T) {
	out := prettyResult(map[string]any{"status": engine.StatusExited, "exit_code": 2}, false)
	if !strings.Contains(out, "❌") || !strings.Contains(out, "exit 2") {
		t.Fatalf("nonzero exit not rendered:\n%s", out)
	}
}

// A still-running job must keep job_id AND next_cursor verbatim in the text view,
// or an agent that reads only content[].text can no longer poll.
func TestPrettyResultRunningKeepsPollHandle(t *testing.T) {
	out := prettyResult(map[string]any{
		"status":      engine.StatusBackgrounded,
		"job_id":      "j_7c1",
		"next_cursor": "42",
	}, false)
	for _, want := range []string{"⏳", "job j_7c1", "exec_poll", `job_id="j_7c1"`, `cursor="42"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPrettyResultAwaitingConfirmation(t *testing.T) {
	out := prettyResult(map[string]any{
		"status":          engine.StatusAwaitingConfirmation,
		"confirmation_id": "c_2b4",
		"reason":          "matched deploy*",
		"job_id":          "j_9",
	}, false)
	for _, want := range []string{"🔒", "c_2b4", "approv", "matched deploy*"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPrettyResultAwaitingInput(t *testing.T) {
	out := prettyResult(map[string]any{
		"status":         engine.StatusAwaitingInput,
		"awaiting_input": true,
		"prompt":         "Password:",
		"job_id":         "j_3",
	}, false)
	for _, want := range []string{"⌨️", "Password:", "exec_write"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPrettyResultError(t *testing.T) {
	e := &errs.Error{Code: errs.DeniedByPolicy, Message: "command denied by policy (matched rm)", Hint: "adjust the command"}
	out := prettyResult(map[string]any{"error": e}, true)
	for _, want := range []string{"❌", "denied_by_policy", "matched rm", "adjust the command"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// Any field the lean shapers add later that prettyStatus doesn't render
// explicitly must still appear, so the text view never silently drops data.
func TestPrettyResultLosslessUnknownField(t *testing.T) {
	out := prettyResult(map[string]any{
		"status":       engine.StatusRunning,
		"job_id":       "j1",
		"future_field": "xyz",
	}, false)
	if !strings.Contains(out, "future_field") || !strings.Contains(out, "xyz") {
		t.Fatalf("unknown field dropped:\n%s", out)
	}
}

func TestPrettyResultFileRead(t *testing.T) {
	out := prettyResult(map[string]any{"content": "hello data", "size": int64(10)}, false)
	for _, want := range []string{"📄", "10", "hello data"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// Non-status, non-file results (capabilities, lists) fall back to JSON — still
// valid, just not specially formatted.
func TestPrettyResultJSONFallback(t *testing.T) {
	out := prettyResult(map[string]any{"servers": []string{"prod", "stage"}}, false)
	if !strings.Contains(out, "servers") || !strings.Contains(out, "prod") {
		t.Fatalf("json fallback lost data:\n%s", out)
	}
}
