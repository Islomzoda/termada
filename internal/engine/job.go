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
	background    bool // explicit mode=background, or auto-backgrounded by Run (daemon heuristic/timeout); excluded from the foreground quota, counted against MaxBackgroundJobs instead
	createdAt     time.Time
	startedAt     time.Time
	endedAt       time.Time
	lastOutput    time.Time
	lastInput     time.Time
	promptInput   time.Time
	lineEpoch     uint64
	promptEpoch   uint64
	promptPrefix  string

	clean              *output.Buffer // cleaned-for-agent (cursor indexes this)
	cleaner            *output.Cleaner
	redactor           *output.Redactor
	secretReservations []*output.LiteralReservation

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
func (j *Job) activate() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status.Terminal() {
		return false
	}
	j.status = StatusRunning
	j.startedAt = time.Now()
	return true
}

func (j *Job) setConfirmID(id string) {
	j.mu.Lock()
	j.confirmID = id
	j.mu.Unlock()
}

// setHold updates the human-intervention flags. A nil pointer leaves that flag
// unchanged.
func (j *Job) setHold(input, output *bool) (changed, allowed bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status.Terminal() || j.status == StatusAwaitingConfirmation {
		return false, false
	}
	if input != nil {
		changed = changed || j.holdInput != *input
		j.holdInput = *input
	}
	if output != nil {
		changed = changed || j.holdOutput != *output
		j.holdOutput = *output
	}
	return changed, true
}

func (j *Job) holds() (input, output bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.holdInput, j.holdOutput
}

// markBackground flags the job as background (for the MaxBackgroundJobs quota
// instead of MaxForegroundJobs). Idempotent.
func (j *Job) markBackground() {
	j.mu.Lock()
	j.background = true
	j.mu.Unlock()
}

func (j *Job) isBackground() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.background
}

// appendOutput stores the cleaned+redacted stream for the agent. Called only by
// the session reader for the current job.
func (j *Job) appendOutput(p []byte) {
	if len(p) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	cleaned := j.cleaner.Clean(p)
	for _, b := range p {
		if b == '\n' || b == '\r' {
			j.lineEpoch++
		}
	}
	if len(cleaned) > 0 {
		_, _ = j.clean.Write([]byte(j.redactor.Redact(string(cleaned))))
	}
	j.lastOutput = time.Now()
}

func (j *Job) inputBoundary() (time.Time, string, uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return time.Now(), j.cleaner.Pending(), j.lineEpoch
}

func (j *Job) recordInput(at time.Time, promptPrefix string, promptEpoch uint64, submitted bool) {
	j.mu.Lock()
	if at.After(j.promptInput) {
		j.promptInput = at
		j.promptPrefix = promptPrefix
		j.promptEpoch = promptEpoch
	}
	if submitted && at.After(j.lastInput) {
		j.lastInput = at
	}
	j.mu.Unlock()
}

// retainSecret keeps a delivered secret registered until final output has been
// redacted. It returns false when the job already reached a terminal state, in
// which case the caller must roll the reservation back immediately.
func (j *Job) retainSecret(reservation *output.LiteralReservation) bool {
	if reservation == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.status.Terminal() {
		return false
	}
	j.secretReservations = append(j.secretReservations, reservation)
	return true
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
	for _, reservation := range j.secretReservations {
		reservation.Rollback()
	}
	j.secretReservations = nil
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
	JobID           string   `json:"job_id"`
	SessionID       string   `json:"session_id"`
	Owner           string   `json:"owner,omitempty"`
	Target          string   `json:"target,omitempty"`
	Workspace       string   `json:"workspace,omitempty"`
	Command         []string `json:"command"`
	Mode            string   `json:"mode,omitempty"`
	Status          Status   `json:"status"`
	ExitCode        *int     `json:"exit_code,omitempty"`
	Signal          string   `json:"signal,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	ConfirmationID  string   `json:"confirmation_id,omitempty"`
	AwaitingInput   bool     `json:"awaiting_input"`
	Prompt          string   `json:"prompt,omitempty"`
	HoldInput       bool     `json:"hold_input"`
	HoldOutput      bool     `json:"hold_output"`
	DurationMS      int64    `json:"duration_ms"`
	CreatedUnix     int64    `json:"created_unix,omitempty"`
	StartedUnix     int64    `json:"started_unix,omitempty"`
	EndedUnix       int64    `json:"ended_unix,omitempty"`
	CreatedUnixMS   int64    `json:"created_unix_ms,omitempty"`
	StartedUnixMS   int64    `json:"started_unix_ms,omitempty"`
	EndedUnixMS     int64    `json:"ended_unix_ms,omitempty"`
	StreamAvailable bool     `json:"stream_available"`
}

func (j *Job) info() Info {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.infoLocked()
}

func (j *Job) readOutput(offset int64, limit int) (Info, []byte, int64, bool, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	chunk, next, gap, hasMore := j.clean.ReadFromLimit(offset, limit)
	return j.infoLocked(), chunk, next, gap, hasMore
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
		JobID:           j.ID,
		SessionID:       j.SessionID,
		Owner:           j.sess.Owner,
		Target:          j.sess.Target,
		Workspace:       j.sess.Workspace,
		Command:         j.Command,
		Mode:            j.Mode,
		Status:          j.status,
		ExitCode:        j.exitCode,
		Signal:          j.signal,
		Reason:          j.reason,
		ConfirmationID:  j.confirmID,
		HoldInput:       j.holdInput,
		HoldOutput:      j.holdOutput,
		CreatedUnix:     unixSeconds(j.createdAt),
		CreatedUnixMS:   unixMilliseconds(j.createdAt),
		StreamAvailable: true,
	}
	if !j.startedAt.IsZero() {
		in.DurationMS = end.Sub(j.startedAt).Milliseconds()
		in.StartedUnix = unixSeconds(j.startedAt)
		in.StartedUnixMS = unixMilliseconds(j.startedAt)
	}
	if !j.endedAt.IsZero() {
		in.EndedUnix = unixSeconds(j.endedAt)
		in.EndedUnixMS = unixMilliseconds(j.endedAt)
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

func unixSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func unixMilliseconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// promptRe matches a trailing interactive prompt: a short last line ending in a
// colon or question mark, a [y/n]-style choice, or a password request. This is a
// deliberately conservative best-effort heuristic (spec IN-1).
var promptRe = regexp.MustCompile(`(?i)(\[y/n\]|\(yes/no\)|password.*:|passphrase.*:|[:?>])\s*$`)

// detectPromptLocked returns the prompt text if the job appears to be waiting
// for input: it has produced output, has been silent briefly, and the tail
// looks like a prompt. Must hold j.mu.
func (j *Job) detectPromptLocked() (string, bool) {
	if j.lastOutput.IsZero() || time.Since(j.lastOutput) < 150*time.Millisecond {
		return "", false
	}
	if !j.lastInput.IsZero() && !j.lastOutput.After(j.lastInput) {
		return "", false
	}
	// Prompts usually have no trailing newline, so they sit in the cleaner's
	// in-progress line. Prefer that; fall back to the last committed line.
	tail := j.cleaner.Pending()
	hadPending := tail != ""
	if j.lastOutput.After(j.promptInput) && j.promptEpoch == j.lineEpoch && j.promptPrefix != "" && strings.HasPrefix(tail, j.promptPrefix) {
		tail = strings.TrimPrefix(tail, j.promptPrefix)
	}
	if strings.TrimSpace(tail) == "" {
		if hadPending {
			return "", false
		}
		total := j.clean.Total()
		from := total - 256
		if from < 0 {
			from = 0
		}
		chunk, _, _, _ := j.clean.ReadFromLimit(from, 256)
		s := strings.TrimRight(string(chunk), "\n")
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		tail = s
	}
	if tail == "" || len(tail) > 200 {
		return "", false
	}
	if promptRe.MatchString(tail) {
		prompt := strings.Trim(tail, " \t")
		return j.redactor.Redact(prompt), true
	}
	return "", false
}
