// Package errs defines the structured error contract surfaced to agents over
// MCP (spec §22b).
package errs

import "fmt"

// Code is a stable machine-readable error code.
type Code string

const (
	NotFound            Code = "not_found"
	DeniedByPolicy      Code = "denied_by_policy"
	SessionBusy         Code = "session_busy"
	VaultLocked         Code = "vault_locked"
	ServerUnreachable   Code = "server_unreachable"
	CursorExpired       Code = "cursor_expired"
	ParallelismExceeded Code = "parallelism_exceeded"
	NotSupported        Code = "not_supported"
	InvalidArgument     Code = "invalid_argument"
	Internal            Code = "internal"
)

// Error is the structured error returned to agents. It marshals to
// {code, message, retriable, hint, details}. The hint is a one-line, actionable
// next step so an agent can recover in a single shot instead of guessing.
type Error struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	Retriable bool           `json:"retriable"`
	Hint      string         `json:"hint,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// New builds a structured error. retriable + hint default from the code.
func New(code Code, format string, args ...any) *Error {
	return &Error{
		Code:      code,
		Message:   fmt.Sprintf(format, args...),
		Retriable: retriableByDefault(code),
		Hint:      hintFor(code),
	}
}

// hintFor returns a one-line recovery hint for the recoverable/common codes.
// Empty for codes whose message already says everything actionable.
func hintFor(code Code) string {
	switch code {
	case SessionBusy:
		return "this session already has a foreground command; run in another session, or exec_poll/exec_kill the current job first"
	case ParallelismExceeded:
		return "you are at your concurrent-job quota; wait for a job to finish or exec_kill one before starting more"
	case VaultLocked:
		return "the vault is locked; a human must run `termada unlock` on the host — agents cannot unlock it"
	case NotFound:
		return "the id no longer exists; list with exec_list / session_list to get a current id"
	case CursorExpired:
		return "output scrolled past the cursor; re-poll with an empty cursor to resync from the start of retained output"
	case ServerUnreachable:
		return "the daemon or remote host did not answer; retry shortly — the session reconnects automatically if it was a transient drop"
	case DeniedByPolicy:
		return "this command is blocked by policy; it cannot be run as-is — a human controls the allow/deny rules"
	default:
		return ""
	}
}

// WithDetails attaches structured details and returns the same error for
// chaining.
func (e *Error) WithDetails(d map[string]any) *Error {
	e.Details = d
	return e
}

func retriableByDefault(code Code) bool {
	switch code {
	case SessionBusy, ServerUnreachable, ParallelismExceeded, VaultLocked:
		return true
	default:
		return false
	}
}
