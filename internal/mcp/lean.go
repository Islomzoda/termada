package mcp

import "github.com/termada/termada/internal/engine"

// The agent-facing responses are deliberately lean: only fields that carry
// signal are emitted, and zero / redundant values are dropped. The agent sent
// the command and session, so we don't echo them back; operator-only flags
// (hold_input/hold_output) and false booleans never reach the agent. This keeps
// every tool result cheap in tokens without hiding anything actionable. The rich
// structs still flow to the dashboard/control-plane unchanged.

// leanRun shapes an exec_run result. A handle (job_id + next_cursor) is included
// only when the command is still going, since that's the only time the agent
// needs to poll.
func leanRun(r *engine.RunResult) map[string]any {
	m := map[string]any{"status": r.Status}
	if r.ExitCode != nil {
		m["exit_code"] = *r.ExitCode
	}
	if r.Stdout != "" {
		m["stdout"] = r.Stdout
	}
	if r.SessionID != "" {
		m["session_id"] = r.SessionID // surfaced so the agent learns/reuses its (default) session
	}
	if r.AwaitingInput {
		m["awaiting_input"] = true
		if r.Prompt != "" {
			m["prompt"] = r.Prompt
		}
	}
	if r.Reason != "" {
		m["reason"] = r.Reason
	}
	if r.ConfirmationID != "" {
		m["confirmation_id"] = r.ConfirmationID
	}
	if r.Truncated {
		m["truncated"] = true
	}
	if !r.Status.Terminal() {
		if r.JobID != "" {
			m["job_id"] = r.JobID
		}
		if r.NextCursor != "" {
			m["next_cursor"] = r.NextCursor
		}
	}
	return m
}

// leanPoll shapes an exec_poll result: the incremental chunk plus just enough
// status. next_cursor is dropped once the job is terminal (nothing left to poll).
func leanPoll(r *engine.PollResult) map[string]any {
	m := map[string]any{"status": r.Status}
	if r.StdoutChunk != "" {
		m["stdout_chunk"] = r.StdoutChunk
	}
	if r.ExitCode != nil {
		m["exit_code"] = *r.ExitCode
	}
	if r.AwaitingInput {
		m["awaiting_input"] = true
		if r.Prompt != "" {
			m["prompt"] = r.Prompt
		}
	}
	if r.Reason != "" {
		m["reason"] = r.Reason
	}
	if r.ConfirmationID != "" {
		// a parked confirm-job stays non-terminal (agent keeps polling); surface
		// the id so poll is consistent with exec_run/exec_start/exec_list.
		m["confirmation_id"] = r.ConfirmationID
	}
	if r.Gap {
		m["gap"] = true
	}
	if r.OutputHeld {
		m["output_held"] = true
	}
	if !r.Status.Terminal() {
		m["next_cursor"] = r.NextCursor
	}
	return m
}

// leanInfo shapes one job for exec_list: enough to identify and triage it,
// without operator flags or false booleans.
func leanInfo(in engine.Info) map[string]any {
	m := map[string]any{
		"job_id":  in.JobID,
		"status":  in.Status,
		"command": in.Command,
	}
	if in.ExitCode != nil {
		m["exit_code"] = *in.ExitCode
	}
	if in.SessionID != "" {
		m["session_id"] = in.SessionID
	}
	if in.DurationMS > 0 {
		m["duration_ms"] = in.DurationMS
	}
	if in.AwaitingInput {
		m["awaiting_input"] = true
	}
	if in.Reason != "" {
		m["reason"] = in.Reason
	}
	if in.ConfirmationID != "" {
		m["confirmation_id"] = in.ConfirmationID
	}
	return m
}

// leanFileRead drops the truncated flag when the whole file was returned.
func leanFileRead(r *engine.FileReadResult) map[string]any {
	m := map[string]any{"content": r.Content, "size": r.Size}
	if r.Truncated {
		m["truncated"] = true
	}
	return m
}
