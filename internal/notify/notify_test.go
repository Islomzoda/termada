package notify

import (
	"strings"
	"testing"
)

type recordRedactor struct{ seen []string }

func (r *recordRedactor) Redact(s string) string {
	r.seen = append(r.seen, s)
	return strings.ReplaceAll(s, "secret", "[redacted]")
}

// Notification text must be run through the redactor before delivery (Telegram
// ships off-box), so a secret in a command line isn't sent to a third party.
func TestNotifyRedactsBeforeSend(t *testing.T) {
	rr := &recordRedactor{}
	n := New(false, TelegramConfig{}, rr) // desktop off, telegram off: no side effects
	n.send("Termada: command denied", "ran mysql -psecret123")

	joined := strings.Join(rr.seen, "|")
	if !strings.Contains(joined, "ran mysql -psecret123") {
		t.Fatalf("body was not passed through the redactor: %v", rr.seen)
	}
	if !strings.Contains(joined, "Termada: command denied") {
		t.Fatalf("title was not passed through the redactor: %v", rr.seen)
	}
}

// A nil redactor must be safe (no masking, no panic).
func TestNotifyNilRedactorOK(t *testing.T) {
	n := New(false, TelegramConfig{}, nil)
	n.send("t", "b") // must not panic
}
