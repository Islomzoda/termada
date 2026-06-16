package engine

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/termada/termada/internal/ids"
	"github.com/termada/termada/internal/output"
)

// Job is a single command execution within a session (spec §9).
type Job struct {
	ID        string
	SessionID string
	Command   []string
	Mode      string

	sess   *Session
	marker string

	mu            sync.Mutex
	status        Status
	exitCode      *int
	signal        string
	reason        string
	confirmID     string
	killRequested bool
	holdInput     bool // human has taken over input; agent writes are blocked
	holdOutput    bool // agent's polled output is paused (human still sees the live stream)
	createdAt     time.Time
	startedAt     time.Time
	endedAt       time.Time
	lastOutput    time.Time

	raw      *output.Buffer // raw-for-replay
	clean    *output.Buffer // cleaned-for-agent (cursor indexes this)
	cleaner  *output.Cleaner
	redactor *output.Redactor

	done chan struct{}
}

func newJob(sess *Session, command []string, mode string) *Job {
	now := time.Now()
	return &Job{
		ID:        ids.New("job"),
		SessionID: sess.ID,
		Command:   command,
		Mode:      mode,
		sess:      sess,
		marker:    ids.Marker(),
		status:    StatusRunning,
		createdAt: now,
		raw:       output.NewBuffer(0),
		clean:     output.NewBuffer(sess.cfg.OutputRetentionBytes),
		cleaner:   &output.Cleaner{},
		redactor:  sess.redactor,
		done:      make(chan struct{}),
	}
}

// newConfirmJob builds a job parked in awaiting_confirmation, not yet executed.
func newConfirmJob(sess *Session, command []string, mode string) *Job {
	j := newJob(sess, command, mode)
	if mode == "" {
		j.Mode = ModeAuto
	}
	j.status = StatusAwaitingConfirmation
	return j
}

// activate transitions a job to running and stamps its start time. Used both
// for normal starts and when a confirmation is approved.
func (j *Job) activate() {
	j.mu.Lock()
	j.status = StatusRunning
	j.startedAt = time.Now()
	j.mu.Unlock()
}

func (j *Job) setConfirmID(id string) {
	j.mu.Lock()
	j.confirmID = id
	j.mu.Unlock()
}

// setHold updates the human-intervention flags. A nil pointer leaves that flag
// unchanged.
func (j *Job) setHold(input, output *bool) {
	j.mu.Lock()
	if input != nil {
		j.holdInput = *input
	}
	if output != nil {
		j.holdOutput = *output
	}
	j.mu.Unlock()
}

func (j *Job) holds() (input, output bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.holdInput, j.holdOutput
}

// appendOutput stores raw output for replay and the cleaned+redacted stream for
// the agent. Called only by the session reader for the current job.
func (j *Job) appendOutput(p []byte) {
	if len(p) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	_, _ = j.raw.Write(p)
	cleaned := j.cleaner.Clean(p)
	if len(cleaned) > 0 {
		_, _ = j.clean.Write([]byte(j.redactor.Redact(string(cleaned))))
	}
	j.lastOutput = time.Now()
}

// finalize moves the job to a terminal state. killStatus, when non-empty,
// overrides the terminal status (e.g. "killed", "timed_out"); reason is an
// optional human-readable explanation.
func (j *Job) finalize(exitCode int, killStatus Status, reason string) {
	j.mu.Lock()
	if j.status.Terminal() {
		j.mu.Unlock()
		return
	}
	// Flush any buffered partial line from the cleaner now that we are at EOF.
	if tail := j.cleaner.Flush(); len(tail) > 0 {
		_, _ = j.clean.Write([]byte(j.redactor.Redact(string(tail))))
	}
	code := exitCode
	j.exitCode = &code
	j.endedAt = time.Now()
	switch {
	case killStatus != "":
		j.status = killStatus
	case j.killRequested:
		j.status = StatusKilled
	default:
		j.status = StatusExited
	}
	if reason != "" {
		j.reason = reason
	}
	j.raw.Close()
	j.clean.Close()
	close(j.done)
	j.mu.Unlock()
}

// requestKill records that a kill was initiated so the eventual marker is
// attributed as a kill, not a normal exit.
func (j *Job) requestKill(signal string) {
	j.mu.Lock()
	j.killRequested = true
	j.signal = signal
	j.mu.Unlock()
}

// Done returns a channel closed when the job reaches a terminal state.
func (j *Job) Done() <-chan struct{} { return j.done }

// Info is the JSON-facing snapshot of a job.
type Info struct {
	JobID          string   `json:"job_id"`
	SessionID      string   `json:"session_id"`
	Command        []string `json:"command"`
	Status         Status   `json:"status"`
	ExitCode       *int     `json:"exit_code,omitempty"`
	Signal         string   `json:"signal,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	ConfirmationID string   `json:"confirmation_id,omitempty"`
	AwaitingInput  bool     `json:"awaiting_input"`
	Prompt         string   `json:"prompt,omitempty"`
	HoldInput      bool     `json:"hold_input"`
	HoldOutput     bool     `json:"hold_output"`
	DurationMS     int64    `json:"duration_ms"`
}

func (j *Job) info() Info {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.infoLocked()
}

// Snapshot returns the job's current Info (exported for callers outside the
// engine package, e.g. the MCP layer).
func (j *Job) Snapshot() Info { return j.info() }

func (j *Job) infoLocked() Info {
	end := j.endedAt
	if end.IsZero() {
		end = time.Now()
	}
	in := Info{
		JobID:          j.ID,
		SessionID:      j.SessionID,
		Command:        j.Command,
		Status:         j.status,
		ExitCode:       j.exitCode,
		Signal:         j.signal,
		Reason:         j.reason,
		ConfirmationID: j.confirmID,
		HoldInput:      j.holdInput,
		HoldOutput:     j.holdOutput,
	}
	if !j.startedAt.IsZero() {
		in.DurationMS = end.Sub(j.startedAt).Milliseconds()
	}
	if j.status == StatusRunning {
		if prompt, ok := j.detectPromptLocked(); ok {
			in.AwaitingInput = true
			in.Status = StatusAwaitingInput
			in.Prompt = prompt
		}
	}
	return in
}

// promptRe matches a trailing interactive prompt: a short last line ending in a
// colon or question mark, a [y/n]-style choice, or a password request. This is a
// deliberately conservative best-effort heuristic (spec IN-1).
var promptRe = regexp.MustCompile(`(?i)(\[y/n\]|\(yes/no\)|password.*:|passphrase.*:|[:?>]\s*)$`)

// detectPromptLocked returns the prompt text if the job appears to be waiting
// for input: it has produced output, has been silent briefly, and the tail
// looks like a prompt. Must hold j.mu.
func (j *Job) detectPromptLocked() (string, bool) {
	if j.lastOutput.IsZero() || time.Since(j.lastOutput) < 150*time.Millisecond {
		return "", false
	}
	total := j.clean.Total()
	from := total - 256
	if from < 0 {
		from = 0
	}
	chunk, _, _ := j.clean.ReadFrom(from)
	tail := strings.TrimRight(string(chunk), "\n")
	if i := strings.LastIndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	if tail == "" || len(tail) > 200 {
		return "", false
	}
	if promptRe.MatchString(tail) {
		return tail, true
	}
	return "", false
}
