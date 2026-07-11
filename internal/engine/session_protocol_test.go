package engine

import (
	"testing"

	"github.com/termada/termada/internal/output"
)

func TestCommandBeginMarkerDropsInteractiveShellChatter(t *testing.T) {
	redactor := output.NewRedactor(nil)
	sess := &Session{
		ID:       "session",
		cfg:      SessionConfig{OutputRetentionBytes: 1024},
		redactor: redactor,
		clean:    output.NewBuffer(1024),
		cleaner:  &output.Cleaner{},
		ready:    true,
	}
	job := newJob(sess, []string{"printf", "\\n0123456789"}, ModeForeground)
	job.activate()
	sess.current = job

	begin := markerBegin(job.marker)
	// Bash 5's readline emits this bracketed-paste disable sequence and newline
	// while accepting a command. Split the begin marker to exercise PTY reads at
	// either side of the protocol boundary.
	sess.consume(append([]byte("\x1b[?2004l\r\n"), begin[:len(begin)-2]...))
	if got, _, _ := job.clean.ReadFrom(0); len(got) != 0 {
		t.Fatalf("pre-command shell chatter leaked into stdout: %q", got)
	}
	sess.consume(append(append([]byte(nil), begin[len(begin)-2:]...), []byte("\n0123456789")...))
	sess.consume([]byte("\x1eTERMADA:" + job.marker + ":0\x1e"))

	got, _, gap := job.clean.ReadFrom(0)
	if gap || string(got) != "\n0123456789" {
		t.Fatalf("stdout = %q gap=%v, want exact command output including leading newline", got, gap)
	}
	if info := job.info(); info.Status != StatusExited || info.ExitCode == nil || *info.ExitCode != 0 {
		t.Fatalf("job did not finalize from end marker: %+v", info)
	}
}
