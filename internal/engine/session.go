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
	shell    *ptyShell

	mu      sync.Mutex
	current *Job
	scan    []byte
	closed  bool
}

// newSession spawns the shell, starts the reader, and runs the init sequence
// (disable echo, enable job control). It blocks until init completes so the
// session is ready for jobs on return.
func newSession(owner, target, mode string, cfg SessionConfig, redactor *output.Redactor) (*Session, error) {
	shell, err := startShell(200, 50)
	if err != nil {
		return nil, errs.New(errs.Internal, "start shell: %v", err)
	}
	s := &Session{
		ID:        ids.New("sess"),
		Target:    target,
		Mode:      mode,
		Owner:     owner,
		CreatedAt: time.Now(),
		cfg:       cfg,
		redactor:  redactor,
		shell:     shell,
	}
	go s.readLoop()

	// Init: disable echo so command/marker lines are not reflected; disable the
	// NL->CRNL output mapping (onlcr) so captured output keeps clean \n; and
	// enable job control so each command gets its own process group (for clean
	// kills).
	job, err := s.runRaw("stty -echo -onlcr 2>/dev/null; set -m 2>/dev/null; true", nil, "init")
	if err != nil {
		shell.close()
		return nil, err
	}
	select {
	case <-job.Done():
	case <-time.After(5 * time.Second):
		shell.close()
		return nil, errs.New(errs.Internal, "session init timed out")
	}
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

// runRaw writes a raw command line plus the completion marker to the shell. It
// returns immediately (async, spec EX-2); the reader finalizes the job when the
// marker appears.
func (s *Session) runRaw(line string, command []string, mode string) (*Job, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errs.New(errs.NotFound, "session closed")
	}
	if s.current != nil {
		s.mu.Unlock()
		return nil, errs.New(errs.SessionBusy, "session %s is running another command", s.ID)
	}
	job := newJob(s, command, mode)
	if mode == "" {
		job.Mode = ModeAuto
	}
	s.current = job
	s.mu.Unlock()

	// Command and marker share ONE shell input line so the marker printf is not
	// left in the PTY input queue where a command that reads stdin (e.g. `read`)
	// would consume it. After the command runs, printf emits:
	//   RS "TERMADA:" <marker> ":" <exit> RS
	payload := line + "; " +
		fmt.Sprintf("printf '\\036TERMADA:%s:%%d\\036' \"$?\"\n", job.marker)
	if _, err := s.shell.f.Write([]byte(payload)); err != nil {
		s.mu.Lock()
		s.current = nil
		s.mu.Unlock()
		job.finalize(-1, StatusFailed, "pty write: "+err.Error())
		return job, errs.New(errs.Internal, "pty write: %v", err)
	}
	job.markStarted()
	return job, nil
}

// readLoop reads the PTY forever, dispatching bytes to the current job and
// detecting completion markers.
func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.shell.f.Read(buf)
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
			// No marker yet. Flush everything except a safety tail that might hold
			// a partial marker, so streaming stays responsive without risking
			// emitting half a marker as output.
			keep := len(open) + 24
			if len(s.scan) > keep {
				job.appendOutput(s.scan[:len(s.scan)-keep])
				s.scan = append([]byte(nil), s.scan[len(s.scan)-keep:]...)
			}
			return
		}
		rest, code, ok := parseMarker(s.scan[idx:], open)
		if !ok {
			// Opening found but exit code/closing delimiter not yet complete.
			if idx > 0 {
				job.appendOutput(s.scan[:idx])
				s.scan = append([]byte(nil), s.scan[idx:]...)
			}
			return
		}
		job.appendOutput(s.scan[:idx])
		job.finalize(code, "", "")
		s.scan = append([]byte(nil), rest...)
		s.current = nil
		// Loop again in case more data trails the marker.
	}
}

func markerOpen(marker string) []byte {
	return append([]byte{markerDelim}, []byte("TERMADA:"+marker+":")...)
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
	_, err := s.shell.f.Write(b)
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
	s.shell.close()
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
