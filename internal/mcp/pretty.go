package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/termada/termada/internal/errs"
)

// prettyResult renders a tool result as legible text for the human reading the
// chat transcript. It is LOSSLESS: every actionable field the lean shapers emit
// (status, exit_code, ids, cursors, prompts, reasons, flags, stdout) is kept, so
// an agent that only reads content[].text loses nothing — while the exact object
// still rides along in structuredContent for machine consumers. Only the
// presentation changes: a one-line status header instead of a wall of JSON.
func prettyResult(v any, isErr bool) string {
	m, ok := v.(map[string]any)
	if !ok {
		return jsonBlock(v)
	}
	switch {
	case isErr:
		return prettyError(m, v)
	case has(m, "status"):
		return prettyStatus(m)
	case has(m, "content") && has(m, "size"):
		return prettyFileRead(m)
	case bval(m, "ok") && has(m, "bytes"):
		return fmt.Sprintf("✅ wrote %s bytes", sval(m, "bytes"))
	default:
		return jsonBlock(v)
	}
}

// prettyStatus renders an exec_run/exec_poll/exec_list-style result: a status
// header line, then the control hints (poll handle, approval, prompt), then the
// output. Any key not explicitly handled is appended as JSON so nothing is lost.
func prettyStatus(m map[string]any) string {
	seen := map[string]bool{}
	mark := func(keys ...string) {
		for _, k := range keys {
			seen[k] = true
		}
	}

	status := sval(m, "status")
	mark("status")
	header := statusIcon(status, m) + " " + status
	if ec, ok := ival(m, "exit_code"); ok {
		mark("exit_code")
		header += fmt.Sprintf(" · exit %d", ec)
	}
	if s := sval(m, "session_id"); s != "" {
		mark("session_id")
		header += " · session " + s
	}
	if s := sval(m, "job_id"); s != "" {
		mark("job_id")
		header += " · job " + s
	}
	if d, ok := ival(m, "duration_ms"); ok {
		mark("duration_ms")
		header += " · " + dur(d)
	}
	if w, ok := ival(m, "waited_ms"); ok {
		mark("waited_ms")
		if budget, ok := ival(m, "timeout_ms"); ok {
			mark("timeout_ms")
			header += " · waited " + dur(w) + "/" + dur(budget)
		} else {
			header += " · waited " + dur(w)
		}
	}

	lines := []string{header}

	if cmd := cmdString(m["command"]); cmd != "" {
		mark("command")
		lines = append(lines, "   $ "+cmd)
	}
	if bval(m, "awaiting_input") {
		mark("awaiting_input")
		line := "   waiting for input"
		if p := sval(m, "prompt"); p != "" {
			mark("prompt")
			line += " — " + oneLine(p)
		}
		lines = append(lines, line, `   answer with exec_write(job_id, input) (secret:true for passwords)`)
	}
	if cid := sval(m, "confirmation_id"); cid != "" {
		mark("confirmation_id")
		lines = append(lines,
			"   needs HUMAN approval — confirmation_id "+cid,
			"   tell the user in chat what this will do and that it needs approval (dashboard/CLI);",
			"   you cannot self-approve, and it auto-denies on timeout")
	}
	if r := sval(m, "reason"); r != "" {
		mark("reason")
		lines = append(lines, "   reason: "+oneLine(r))
	}
	if nc := sval(m, "next_cursor"); nc != "" {
		mark("next_cursor")
		lines = append(lines, fmt.Sprintf(`   → exec_poll(job_id=%q, cursor=%q)`, sval(m, "job_id"), nc))
	}
	if bval(m, "truncated") {
		mark("truncated")
		lines = append(lines, "   … output truncated")
	}
	if bval(m, "gap") {
		mark("gap")
		lines = append(lines, "   … output gap (some bytes dropped before this cursor)")
	}
	if bval(m, "output_held") {
		mark("output_held")
		lines = append(lines, "   (output held by operator)")
	}

	body := sval(m, "stdout")
	mark("stdout")
	if body == "" {
		body = sval(m, "stdout_chunk")
		mark("stdout_chunk")
	}

	// Anything the lean shapers add later that we don't render explicitly still
	// shows up verbatim, so the text view can never silently drop a field.
	extra := map[string]any{}
	for k, val := range m {
		if !seen[k] {
			extra[k] = val
		}
	}

	out := strings.Join(lines, "\n")
	if body != "" {
		out += "\n\n" + body
	}
	if len(extra) > 0 {
		out += "\n" + jsonBlock(extra)
	}
	return out
}

func prettyError(m map[string]any, v any) string {
	e, ok := m["error"].(*errs.Error)
	if !ok {
		return "❌ " + jsonBlock(v)
	}
	out := "❌ " + string(e.Code)
	if e.Message != "" {
		out += ": " + e.Message
	}
	if e.Retriable {
		out += " (retriable)"
	}
	if e.Hint != "" {
		out += "\n   ↳ " + e.Hint
	}
	return out
}

func prettyFileRead(m map[string]any) string {
	head := "📄 " + sval(m, "size") + " bytes"
	if bval(m, "truncated") {
		head += " · truncated"
	}
	if c := sval(m, "content"); c != "" {
		return head + "\n\n" + c
	}
	return head
}

func statusIcon(status string, m map[string]any) string {
	switch status {
	case "exited":
		if ec, ok := ival(m, "exit_code"); ok && ec != 0 {
			return "❌"
		}
		return "✅"
	case "running", "backgrounded":
		return "⏳"
	case "awaiting_input":
		return "⌨️"
	case "awaiting_confirmation":
		return "🔒"
	case "killed", "timed_out", "failed", "orphaned", "denied":
		return "⛔"
	default:
		return "•"
	}
}

func dur(ms int64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}

func cmdString(v any) string {
	switch c := v.(type) {
	case []string:
		return strings.Join(c, " ")
	case []any:
		parts := make([]string, 0, len(c))
		for _, p := range c {
			parts = append(parts, fmt.Sprint(p))
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// oneLine collapses a prompt/reason to a single line so it never breaks the
// header block.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func has(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func bval(m map[string]any, k string) bool { b, _ := m[k].(bool); return b }

func sval(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func ival(m map[string]any, k string) (int64, bool) {
	switch v := m[k].(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	}
	return 0, false
}

func jsonBlock(v any) string {
	text, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return `{"error":"failed to encode result"}`
	}
	return string(text)
}
