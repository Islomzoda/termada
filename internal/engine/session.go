package engine

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/ids"
	"github.com/termada/termada/internal/output"
)

// markerDelim is ASCII RS (record separator), chosen because it is extremely
// unlikely to appear in normal command output.
const markerDelim = 0x1e

const (
	maxSessionInputBytes    = 64 << 10
	commandWriteTimeout     = 30 * time.Second
	interactiveWriteTimeout = 5 * time.Second
)

// SessionConfig holds the per-session knobs the manager passes down.
type SessionConfig struct {
	OutputRetentionBytes int
	PTYCols              int // initial PTY width for a local session (0 = default 200)
}

// SpawnConfig controls how a local agent shell is launched. The zero value runs
// the shell as the daemon's own uid (the default, legacy behaviour). When
// SeparateUID is set the daemon (which must be root) drops the shell to UID/GID
// so an agent's `exec` can't read the daemon's secrets, the control socket, or
// the operator's credential stores — closing the same-uid residual of the
// file/socket guards (spec SEC-8/§3a). It is platform-specific: honoured on Unix,
// rejected on Windows.
type SpawnConfig struct {
	SeparateUID bool
	UID, GID    int
	Username    string
	HomeDir     string
}

// Session is a persistent shell over a PTY that preserves cwd/env/venv between
// commands (spec SS-1/SS-3). One foreground command runs at a time (SS-5).
type Session struct {
	ID        string
	Target    string
	Mode      string
	Owner     string
	CreatedAt time.Time

	cfg      SessionConfig
	redactor *output.Redactor
	shell    ShellConn
	onReset  func() // called after a dropped remote link is reconnected (cwd/env lost)

	// session-wide live terminal buffer: every byte the shell emits (across all
	// jobs) is appended here so the dashboard can show one continuous terminal
	// for the session, like a real shell.
	clean   *output.Buffer
	cleaner *output.Cleaner

	mu             sync.Mutex
	writeMu        sync.Mutex
	closeOnce      sync.Once
	current        *Job
	scan           []byte
	outputStarted  bool // the current job's begin marker has been consumed
	closed         bool
	ready          bool // init finished; session-terminal buffer starts capturing
	onResetPending int
}

// newSession spawns the shell, starts the reader, and runs the init sequence
// (disable echo, enable job control). It blocks until init completes so the
// session is ready for jobs on return.
func newSession(owner, target, mode string, cfg SessionConfig, redactor *output.Redactor, sp SpawnConfig) (*Session, error) {
	cols := cfg.PTYCols
	if cols <= 0 {
		cols = 200
	}
	shell, err := startShell(cols, 50, sp)
	if err != nil {
		return nil, errs.New(errs.Internal, "start shell: %v", err)
	}
	return newSessionConn(owner, target, mode, shell, cfg, redactor)
}

// newSessionConn builds a session over an arbitrary shell transport (local PTY
// or remote SSH) and runs the init sequence over it.
func newSessionConn(owner, target, mode string, shell ShellConn, cfg SessionConfig, redactor *output.Redactor) (*Session, error) {
	s := &Session{
		ID:        ids.New("sess"),
		Target:    target,
		Mode:      mode,
		Owner:     owner,
		CreatedAt: time.Now(),
		cfg:       cfg,
		redactor:  redactor,
		shell:     shell,
		clean:     output.NewBuffer(cfg.OutputRetentionBytes),
		cleaner:   &output.Cleaner{},
	}
	go s.readLoop()

	// Init: disable echo so command/marker lines are not reflected; disable the
	// NL->CRNL output mapping (onlcr) so captured output keeps clean \n; and
	// enable job control so each command gets its own process group (for clean
	// kills).
	job, err := s.runRaw("stty -echo -onlcr 2>/dev/null; set -m 2>/dev/null; true", nil, "init")
	if err != nil {
		s.close()
		return nil, err
	}
	select {
	case <-job.Done():
	case <-time.After(30 * time.Second):
		// Generous: under heavy CI load (many PTYs spawning in parallel, -race
		// slowdown) a shell's first marker round-trip can take several seconds.
		s.close()
		return nil, errs.New(errs.Internal, "session init timed out")
	}
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	return s, nil
}

// exec quotes argv so shell metacharacters are inert (spec R3: argv is the
// boundary; persistent-shell quoting neutralises injection while keeping
// cwd/env persistence) and runs it.
func (s *Session) exec(command []string, mode string) (*Job, error) {
	if len(command) == 0 {
		return nil, errs.New(errs.InvalidArgument, "empty command")
	}
	return s.runRaw(quoteArgv(command), command, mode)
}

// startJob attaches an existing job to the session and writes its command plus
// boundary markers to the shell. It returns immediately (async, spec EX-2); the
// reader finalizes the job when the completion marker appears. Used both for
// fresh jobs and for jobs released from the confirmation queue.
func (s *Session) startJob(job *Job, line string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		err := errs.New(errs.NotFound, "session closed")
		job.finalize(-1, StatusFailed, err.Error())
		return err
	}
	if s.current != nil {
		s.mu.Unlock()
		err := errs.New(errs.SessionBusy, "session %s is running another command", s.ID)
		job.finalize(-1, StatusFailed, err.Error())
		return err
	}
	if !job.activate() {
		s.mu.Unlock()
		return errs.New(errs.NotFound, "job is already terminal")
	}
	s.current = job
	s.outputStarted = false
	s.mu.Unlock()

	// Command and markers share ONE shell input line so the marker printf is not
	// left in the PTY input queue where a command that reads stdin (e.g. `read`)
	// would consume it. The begin marker separates shell/readline UI emitted while
	// accepting the line from actual command output. After the command runs, the
	// second printf emits:
	//   RS "TERMADA:" <marker> ":" <exit> RS
	payload := fmt.Sprintf("printf '\\036TERMADA-BEGIN:%s\\036'; ", job.marker) + line + "; " +
		fmt.Sprintf("printf '\\036TERMADA:%s:%%d\\036' \"$?\"\n", job.marker)
	if err := s.writeCurrent(job, []byte(payload), commandWriteTimeout, false); err != nil {
		s.mu.Lock()
		if s.current == job {
			s.current = nil
			s.outputStarted = false
		}
		s.mu.Unlock()
		job.finalize(-1, StatusFailed, "pty write: "+err.Error())
		return err
	}
	return nil
}

// runRaw creates a fresh job and starts it (used for init and internal raw
// command lines).
func (s *Session) runRaw(line string, command []string, mode string) (*Job, error) {
	job := newJob(s, command, mode)
	if mode == "" {
		job.Mode = ModeAuto
	}
	if err := s.startJob(job, line); err != nil {
		return job, err
	}
	return job, nil
}

// readLoop reads the PTY forever, dispatching bytes to the current job and
// detecting completion markers.
func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.shell.Read(buf)
		if n > 0 {
			s.consume(buf[:n])
		}
		if err != nil {
			s.handleEOF()
			return
		}
	}
}

func (s *Session) handleEOF() {
	if s.isClosed() {
		s.closeTransport()
		return // deliberate close
	}
	// Remote transports can reconnect (the shell survives in tmux on the server);
	// try that before giving up (spec RM-3).
	if rc, ok := s.shell.(interface{ Reconnect() error }); ok {
		if s.tryReconnect(rc) {
			return
		}
	}
	s.terminate(StatusOrphaned, "session shell exited")
}

// tryReconnect re-establishes a dropped remote transport (tmux re-attach). The
// in-flight job is orphaned — its result can't be trusted across the gap — and
// the reader is restarted so the session keeps serving new commands (spec RM-3).
func (s *Session) tryReconnect(rc interface{ Reconnect() error }) bool {
	for attempt := 0; attempt < 5; attempt++ {
		if s.isClosed() {
			return false
		}
		time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
		// Serialize the transport swap with every shell write. Without this lock,
		// Reconnect can install a fresh SSH shell while input for the orphaned job
		// is still in writeCurrent; sshShell.Write would then resolve its current
		// connection to the new shell and type stale input into it.
		s.writeMu.Lock()
		if s.isClosed() {
			s.writeMu.Unlock()
			return false
		}
		if err := rc.Reconnect(); err != nil {
			s.writeMu.Unlock()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			s.writeMu.Unlock()
			return false
		}
		job := s.current
		s.current = nil
		s.outputStarted = false
		s.scan = s.scan[:0]
		s.mu.Unlock()
		if job != nil {
			job.finalize(-1, StatusOrphaned, "connection dropped; session reconnected")
		}
		s.writeMu.Unlock()
		// Surface the reset even when the session was idle: the reconnected shell is
		// fresh, so cwd/env from before the drop are gone — make that observable
		// instead of a silent footgun for the next command.
		s.notifyReset()
		go s.readLoop()
		// re-init the new PTY (echo off, job control); fire-and-forget.
		_, _ = s.runRaw("stty -echo -onlcr 2>/dev/null; set -m 2>/dev/null; true", nil, "init")
		return true
	}
	return false
}

// consume appends new bytes and extracts complete job output up to the marker.
func (s *Session) consume(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scan = append(s.scan, p...)
	for {
		job := s.current
		if job == nil {
			// No job running: discard idle shell chatter.
			s.scan = s.scan[:0]
			return
		}
		if !s.outputStarted {
			begin := markerBegin(job.marker)
			idx := bytes.Index(s.scan, begin)
			if idx < 0 {
				// Interactive shells may emit prompt/readline control traffic after
				// the previous completion marker and while accepting this command.
				// It is outside the command boundary: discard it, retaining only a
				// possible split begin marker.
				discardTo := partialMarkerStart(s.scan, begin)
				if discardTo > 0 {
					s.scan = append([]byte(nil), s.scan[discardTo:]...)
				}
				return
			}
			s.scan = append([]byte(nil), s.scan[idx+len(begin):]...)
			s.outputStarted = true
			continue
		}
		open := markerOpen(job.marker)
		idx := bytes.Index(s.scan, open)
		if idx < 0 {
			// No marker yet. Flush everything except a trailing fragment that could
			// be a forming marker (markers begin with the RS byte, which normal
			// output essentially never contains). This surfaces short prompts that
			// have no newline immediately, instead of waiting for more bytes.
			flushTo := partialMarkerStart(s.scan, open)
			if flushTo > 0 {
				out := s.scan[:flushTo]
				job.appendOutput(out)
				s.sessionAppend(out)
				s.scan = append([]byte(nil), s.scan[flushTo:]...)
			}
			return
		}
		rest, code, ok := parseMarker(s.scan[idx:], open)
		if !ok {
			// Opening found but exit code/closing delimiter not yet complete.
			if idx > 0 {
				job.appendOutput(s.scan[:idx])
				s.sessionAppend(s.scan[:idx])
				s.scan = append([]byte(nil), s.scan[idx:]...)
			}
			return
		}
		job.appendOutput(s.scan[:idx])
		s.sessionAppend(s.scan[:idx])
		job.finalize(code, "", "")
		s.scan = append([]byte(nil), rest...)
		s.current = nil
		s.outputStarted = false
		// Loop again in case more data trails the marker.
	}
}

// sessionAppend feeds job output into the session-wide live terminal buffer
// (cleaned + redacted). Called by the reader goroutine only.
func (s *Session) sessionAppend(p []byte) {
	if len(p) == 0 || !s.ready {
		return
	}
	cleaned := s.cleaner.Clean(p)
	if len(cleaned) > 0 {
		_, _ = s.clean.Write([]byte(s.redactor.Redact(string(cleaned))))
	}
}

func markerOpen(marker string) []byte {
	return append([]byte{markerDelim}, []byte("TERMADA:"+marker+":")...)
}

func markerBegin(marker string) []byte {
	return append(append([]byte{markerDelim}, []byte("TERMADA-BEGIN:"+marker)...), markerDelim)
}

// partialMarkerStart returns the index up to which scan can be safely flushed as
// output: if a trailing fragment looks like the start of a forming marker (a
// prefix of the open sequence, or the open sequence followed by partial digits),
// hold back from there; otherwise the whole buffer is safe to flush.
func partialMarkerStart(scan, open []byte) int {
	k := bytes.LastIndexByte(scan, markerDelim)
	if k < 0 {
		return len(scan)
	}
	tail := scan[k:]
	if bytes.HasPrefix(open, tail) || bytes.HasPrefix(tail, open) {
		return k // a possible forming marker — hold it back
	}
	return len(scan)
}

// parseMarker parses "<RS>TERMADA:<marker>:<digits><RS>" at the start of s. It
// returns the remaining bytes after the marker, the exit code, and ok=false if
// the marker is not yet complete.
func parseMarker(s, open []byte) (rest []byte, code int, ok bool) {
	if !bytes.HasPrefix(s, open) {
		return nil, 0, false
	}
	j := len(open)
	k := j
	for k < len(s) && s[k] >= '0' && s[k] <= '9' {
		k++
	}
	if k >= len(s) {
		return nil, 0, false // digits/closing delimiter still arriving
	}
	if k == j || s[k] != markerDelim {
		return nil, 0, false
	}
	code, _ = strconv.Atoi(string(s[j:k]))
	return s[k+1:], code, true
}

// writeInput writes raw bytes to the PTY master (spec EX-4/M19: input including
// passwords goes to the PTY, not a stdin pipe).
func (s *Session) writeInput(job *Job, b []byte) error {
	if len(b) > maxSessionInputBytes {
		return errs.New(errs.InvalidArgument, "input exceeds %d byte limit", maxSessionInputBytes)
	}
	return s.writeCurrent(job, b, interactiveWriteTimeout, true)
}

type shellWriteResult struct {
	n   int
	err error
}

func (s *Session) writeCurrent(job *Job, data []byte, timeout time.Duration, verifyAfter bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errs.New(errs.NotFound, "session closed")
	}
	if job != nil && s.current != job {
		s.mu.Unlock()
		return errs.New(errs.NotFound, "job is no longer current")
	}
	shell := s.shell
	s.mu.Unlock()

	payload := append([]byte(nil), data...)
	done := make(chan shellWriteResult, 1)
	var writeFinished atomic.Bool
	go func() {
		n, err := shell.Write(payload)
		// Publish completion before the channel send. If job.Done and done become
		// ready together, the waiter can distinguish a completed write from one
		// that is still capable of leaking bytes into an idle/future shell.
		writeFinished.Store(true)
		done <- shellWriteResult{n: n, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var jobDone <-chan struct{}
	if verifyAfter && job != nil {
		jobDone = job.Done()
	}
	var result shellWriteResult
	select {
	case result = <-done:
	case <-timer.C:
		s.closeTransport()
		select {
		case result = <-done:
		case <-time.After(time.Second):
		}
		return errs.New(errs.Internal, "PTY write timed out after %s; session transport closed", timeout)
	case <-jobDone:
		if writeFinished.Load() {
			result = <-done
			break
		}
		select {
		case result = <-done:
			// The full write completed before we acted on the terminal event. This
			// is the normal fast-path for input that immediately completes `read`;
			// do not tear down a healthy persistent shell merely because both
			// notifications became ready together.
			break
		default:
			s.closeTransport()
			select {
			case result = <-done:
			case <-time.After(time.Second):
			}
			return errs.New(errs.NotFound, "job completed while input was being written; session transport closed")
		}
	}
	if result.err != nil {
		return errs.New(errs.Internal, "pty write: %v", result.err)
	}
	if result.n != len(payload) {
		return errs.New(errs.Internal, "short PTY write: wrote %d of %d bytes", result.n, len(payload))
	}
	if verifyAfter && job != nil {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return errs.New(errs.NotFound, "session closed while input was being written")
		}
	} else {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return errs.New(errs.NotFound, "session closed while command was being written")
		}
	}
	return nil
}

func (s *Session) currentJob() *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) close() {
	s.terminate(StatusKilled, "session closed")
}

// terminate performs the one-way logical close and always drives the transport
// through its exactly-once cleanup path. EOF and an explicit close can race; the
// first transition owns the job status, while either caller may reap the backend.
func (s *Session) terminate(status Status, reason string) {
	s.mu.Lock()
	var job *Job
	if !s.closed {
		s.closed = true
		job = s.current
		s.current = nil
		s.outputStarted = false
	}
	s.mu.Unlock()
	if job != nil {
		job.finalize(-1, status, reason)
	}
	s.closeTransport()
}

func (s *Session) closeTransport() {
	s.closeOnce.Do(func() { _ = s.shell.Close() })
}

// setOnReset installs the manager callback and drains resets that happened
// during session construction. The callback is invoked without s.mu held.
func (s *Session) setOnReset(callback func()) {
	s.mu.Lock()
	s.onReset = callback
	pending := s.onResetPending
	s.onResetPending = 0
	s.mu.Unlock()
	for i := 0; i < pending; i++ {
		callback()
	}
}

func (s *Session) notifyReset() {
	s.mu.Lock()
	callback := s.onReset
	if callback == nil {
		s.onResetPending++
	}
	s.mu.Unlock()
	if callback != nil {
		callback()
	}
}

// quoteArgv joins argv into a single shell command line with each argument
// safely single-quoted, so metacharacters are literal.
func quoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
