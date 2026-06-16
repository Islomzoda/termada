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
// {code, message, retriable, details}.
type Error struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	Retriable bool           `json:"retriable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// New builds a structured error. retriable defaults follow the code: transient
// codes are retriable, the rest are not.
func New(code Code, format string, args ...any) *Error {
	return &Error{
		Code:      code,
		Message:   fmt.Sprintf(format, args...),
		Retriable: retriableByDefault(code),
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
