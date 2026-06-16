package engine

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/termada/termada/internal/errs"
	"github.com/termada/termada/internal/ids"
	"github.com/termada/termada/internal/output"
)

// markerDelim is ASCII RS (record separator), chosen because it is extremely
// unlikely to appear in normal command output.
const markerDelim = 0x1e

// SessionConfig holds the per-session knobs the manager passes down.
type SessionConfig struct {
	OutputRetentionBytes int
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

	// session-wide live terminal buffer: every byte the shell emits (across all
	// jobs) is appended here so the dashboard can show one continuous terminal
	// for the session, like a real shell.
	clean   *output.Buffer
	cleaner *output.Cleaner

	mu      sync.Mutex
	current *Job
	scan    []byte
	closed  bool
	ready   bool // init finished; session-terminal buffer starts capturing
}

// newSession spawns the shell, starts the reader, and runs the init sequence
// (disable echo, enable job control). It blocks until init completes so the
// session is ready for jobs on return.
func newSession(owner, target, mode string, cfg SessionConfig, redactor *output.Redactor) (*Session, error) {
	shell, err := startShell(200, 50)
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
		shell.Close()
		return nil, err
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		shell.Close()
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
// completion marker to the shell. It returns immediately (async, spec EX-2); the
// reader finalizes the job when the marker appears. Used both for fresh jobs and
// for jobs released from the confirmation queue.
func (s *Session) startJob(job *Job, line string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errs.New(errs.NotFound, "session closed")
	}
	if s.current != nil {
		s.mu.Unlock()
		return errs.New(errs.SessionBusy, "session %s is running another command", s.ID)
	}
	s.current = job
	s.mu.Unlock()

	// Command and marker share ONE shell input line so the marker printf is not
	// left in the PTY input queue where a command that reads stdin (e.g. `read`)
	// would consume it. After the command runs, printf emits:
	//   RS "TERMADA:" <marker> ":" <exit> RS
	payload := line + "; " +
		fmt.Sprintf("printf '\\036TERMADA:%s:%%d\\036' \"$?\"\n", job.marker)
	if _, err := s.shell.Write([]byte(payload)); err != nil {
		s.mu.Lock()
		s.current = nil
		s.mu.Unlock()
		job.finalize(-1, StatusFailed, "pty write: "+err.Error())
		return errs.New(errs.Internal, "pty write: %v", err)
	}
	job.activate()
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
	s.mu.Lock()
	job := s.current
	s.current = nil
	s.closed = true
	s.mu.Unlock()
	if job != nil {
		// The shell died with a command still running: it is orphaned, not exited.
		job.finalize(-1, StatusOrphaned, "session shell exited")
	}
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
func (s *Session) writeInput(b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errs.New(errs.NotFound, "session closed")
	}
	_, err := s.shell.Write(b)
	if err != nil {
		return errs.New(errs.Internal, "pty write: %v", err)
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
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	job := s.current
	s.current = nil
	s.mu.Unlock()
	if job != nil {
		job.finalize(-1, StatusKilled, "session closed")
	}
	_ = s.shell.Close()
}

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

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
	if s == "" {
		return "''"
	}
	if safeArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
