package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/termada/termada/internal/output"
)

func TestAuditChainAndVerify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path, output.NewRedactor(nil))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := l.Append(Record{Type: "job.started", AgentID: "a", Message: "echo hi"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	l.Close()

	n, err := Verify(path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if n != 5 {
		t.Fatalf("verified %d records, want 5", n)
	}
}

func TestAuditDetectsTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, _ := Open(path, output.NewRedactor(nil))
	_ = l.Append(Record{Type: "job.started", Message: "original"})
	_ = l.Append(Record{Type: "job.finished", Message: "second"})
	l.Close()

	data, _ := os.ReadFile(path)
	tampered := strings.Replace(string(data), "original", "MALICIOUS", 1)
	if tampered == string(data) {
		t.Fatal("test setup: replacement did not occur")
	}
	_ = os.WriteFile(path, []byte(tampered), 0o600)

	if _, err := Verify(path); err == nil {
		t.Fatal("Verify should detect the altered record")
	}
}

func TestAuditRecoversChainAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, _ := Open(path, output.NewRedactor(nil))
	_ = l.Append(Record{Type: "a", Message: "one"})
	l.Close()

	l2, err := Open(path, output.NewRedactor(nil))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = l2.Append(Record{Type: "b", Message: "two"})
	l2.Close()

	n, err := Verify(path)
	if err != nil {
		t.Fatalf("verify after reopen: %v", err)
	}
	if n != 2 {
		t.Fatalf("verified %d, want 2 (chain must continue across reopen)", n)
	}
}

func TestAuditRedactsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, _ := Open(path, output.NewRedactor(nil))
	_ = l.Append(Record{Type: "x", Message: "token ghp_abcdefghijklmnopqrstuvwxyz0"})
	l.Close()
	recs, _ := l.Tail(10)
	if len(recs) != 1 || strings.Contains(recs[0].Message, "ghp_abcdefghijklmnopqrstuvwxyz0") {
		t.Fatalf("secret not redacted in audit: %q", recs[0].Message)
	}
}
