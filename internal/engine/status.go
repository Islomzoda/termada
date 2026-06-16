package engine

// Status is the single source of truth for a job's lifecycle (spec §22a).
type Status string

const (
	StatusRunning              Status = "running"
	StatusAwaitingInput        Status = "awaiting_input"
	StatusAwaitingConfirmation Status = "awaiting_confirmation"
	StatusExited               Status = "exited"
	StatusKilled               Status = "killed"
	StatusTimedOut             Status = "timed_out"
	StatusFailed               Status = "failed"
	StatusOrphaned             Status = "orphaned"
	StatusBackgrounded         Status = "backgrounded"
)

// Terminal reports whether the status is final (the job will never run again).
// backgrounded is NOT terminal: the job is still alive, control was just handed
// back to the agent.
func (s Status) Terminal() bool {
	switch s {
	case StatusExited, StatusKilled, StatusTimedOut, StatusFailed, StatusOrphaned:
		return true
	default:
		return false
	}
}

// Mode is the execution intent for a job (spec EX-7 / LR-1).
const (
	ModeAuto       = "auto"
	ModeForeground = "foreground"
	ModeBackground = "background"
)
