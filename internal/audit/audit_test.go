package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/termada/termada/internal/bus"
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

func TestAuditRotationKeepsChainVerifiable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, _ := Open(path, output.NewRedactor(nil))
	l.SetMaxBytes(600) // tiny → forces several rotations
	for i := 0; i < 60; i++ {
		if err := l.Append(Record{Type: "job.started", Message: "a reasonably long message to grow the file"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	l.Close()

	// rotation actually happened
	segs, _ := filepath.Glob(path + ".*")
	if len(segs) == 0 {
		t.Fatalf("expected at least one sealed segment, got none")
	}
	// active log verifies
	if _, err := Verify(path); err != nil {
		t.Fatalf("active log verify: %v", err)
	}
	// every sealed segment verifies independently (chain intact within each)
	for _, seg := range segs {
		if _, err := Verify(seg); err != nil {
			t.Fatalf("segment %s verify: %v", seg, err)
		}
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

func TestAuditRecursivelyRedactsStructuredData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	r := output.NewRedactor(nil)
	if err := r.AddLiteral("deep-secret"); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, r)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"commands": []string{"echo ok", "printf deep-secret"},
		"nested": []any{map[string]any{
			"value": "deep-secret",
		}},
		"typed_maps": []map[string]any{{"error": "deep-secret"}},
	}
	if err := l.Append(Record{Type: "fleet.started", Data: data}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "deep-secret") {
		t.Fatalf("nested secret written to audit log: %s", raw)
	}
	// The caller's event is not mutated while producing the redacted copy.
	if got := data["commands"].([]string)[1]; got != "printf deep-secret" {
		t.Fatalf("input data mutated: %q", got)
	}
}

func TestAuditFailureIsSurfacedAndHealthIsLatched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path, output.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	err = l.Append(Record{Type: "bad", Data: map[string]any{"unsupported": make(chan int)}})
	if err == nil {
		t.Fatal("Append succeeded with unsupported structured data")
	}
	if l.Healthy() {
		t.Fatal("logger remained healthy after a dropped audit record")
	}
	if l.LastError() == nil {
		t.Fatal("LastError is nil after failed append")
	}
	if nextErr := l.Append(Record{Type: "later"}); nextErr == nil {
		t.Fatal("unhealthy logger accepted another record")
	}
	if closeErr := l.Close(); closeErr != nil {
		t.Fatalf("close after marshal failure: %v", closeErr)
	}
	if n, verifyErr := Verify(path); verifyErr != nil || n != 0 {
		t.Fatalf("Verify after marshal failure = (%d, %v), want (0, nil)", n, verifyErr)
	}
}

func TestOpenRejectsTamperedExistingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path, output.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Record{Type: "test", Message: "original"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "original", "tampered", 1))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path, output.NewRedactor(nil)); err == nil {
		reopened.Close()
		t.Fatal("Open accepted an audit log with an invalid hash")
	}
}

func writeRotatedAudit(t *testing.T, path string) int64 {
	t.Helper()
	l, err := Open(path, output.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	l.SetMaxBytes(500)
	for i := 0; i < 80; i++ {
		if err := l.Append(Record{Type: "job.started", Message: "long enough to rotate audit segments"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return 80
}

func TestVerifyAllChecksRotatedChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	want := writeRotatedAudit(t, path)
	segments, err := sealedSegments(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) < 3 {
		t.Fatalf("test setup produced %d sealed segments, want at least 3", len(segments))
	}
	if got, err := VerifyAll(path); err != nil || got != want {
		t.Fatalf("VerifyAll = (%d, %v), want (%d, nil)", got, err, want)
	}
}

func TestReopenContinuesAcrossRotatedSegments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	want := writeRotatedAudit(t, path)

	l, err := Open(path, output.NewRedactor(nil))
	if err != nil {
		t.Fatalf("reopen rotated log: %v", err)
	}
	if err := l.Append(Record{Type: "after-restart"}); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := VerifyAll(path); err != nil || got != want+1 {
		t.Fatalf("VerifyAll after reopen = (%d, %v), want (%d, nil)", got, err, want+1)
	}
}

func TestOpenRejectsTamperedSealedSegment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writeRotatedAudit(t, path)
	segments, err := sealedSegments(path)
	if err != nil || len(segments) == 0 {
		t.Fatalf("sealedSegments = (%v, %v)", segments, err)
	}
	raw, err := os.ReadFile(segments[0].path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "job.started", "job.changed", 1))
	if err := os.WriteFile(segments[0].path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path, output.NewRedactor(nil)); err == nil {
		_ = reopened.Close()
		t.Fatal("Open accepted a tampered sealed audit segment")
	}
}

func TestVerifyAllDetectsDeletedSegment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writeRotatedAudit(t, path)
	segments, err := sealedSegments(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) < 3 {
		t.Fatalf("test setup produced %d sealed segments, want at least 3", len(segments))
	}
	if err := os.Remove(segments[len(segments)/2].path); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAll(path); err == nil {
		t.Fatal("VerifyAll accepted a chain with a deleted segment")
	}
}

func TestVerifyAllDetectsRenamedSegmentOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writeRotatedAudit(t, path)
	segments, err := sealedSegments(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) < 2 {
		t.Fatalf("test setup produced %d sealed segments, want at least 2", len(segments))
	}
	tmp := segments[0].path + ".swap"
	if err := os.Rename(segments[0].path, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(segments[1].path, segments[0].path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, segments[1].path); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAll(path); err == nil {
		t.Fatal("VerifyAll accepted sealed segments renamed into a different order")
	}
}

func TestPostRedactionOversizedAuditRecordRejectsOnlyCurrentRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	redactor := output.NewRedactor(nil)
	if err := redactor.AddLiteral("x"); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path, redactor)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Record{Type: "oversized", Message: strings.Repeat("x", maxAuditRecordBytes+1)}); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("oversized audit record error = %v, want %v", err, ErrRecordTooLarge)
	}
	if !l.Healthy() {
		t.Fatalf("oversized caller input poisoned audit health: %v", l.LastError())
	}
	if err := l.Append(Record{Type: "later", Message: "bounded"}); err != nil {
		t.Fatalf("bounded append after rejection: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := Verify(path); err != nil || n != 1 {
		t.Fatalf("Verify after oversized rejection = (%d, %v), want (1, nil)", n, err)
	}
}

func TestVerifyAllRejectsMissingActiveLogAfterCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	logger, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger.SetMaxBytes(350)
	logger.SetMaxSegments(1)
	for i := 0; i < 30; i++ {
		if err := logger.Append(Record{Type: "event", Message: "long enough to rotate"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := loadArchiveCheckpoint(path); err != nil || !ok {
		t.Fatalf("loadArchiveCheckpoint = (_, %v, %v), want (_, true, nil)", ok, err)
	}
	segments, err := sealedSegments(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range segments {
		if err := os.Remove(segment.path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAll(path); err == nil {
		t.Fatal("VerifyAll accepted a missing active log after retention checkpointing")
	}
	if reopened, err := Open(path, nil); err == nil {
		_ = reopened.Close()
		t.Fatal("Open recreated a missing active log after retention checkpointing")
	}
}

func TestVerifyAllRejectsSymlinkedSealedSegment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writeRotatedAudit(t, path)
	segments, err := sealedSegments(path)
	if err != nil || len(segments) == 0 {
		t.Fatalf("sealedSegments = (%v, %v)", segments, err)
	}
	original := filepath.Join(filepath.Dir(path), "sealed-segment-original")
	if err := os.Rename(segments[0].path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, segments[0].path); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := VerifyAll(path); err == nil {
		t.Fatal("VerifyAll accepted a symlinked sealed audit segment")
	}
}

func TestRedactionExpansionCannotPoisonReliableAuditSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	redactor := output.NewRedactor(nil)
	if err := redactor.AddLiteral("x"); err != nil {
		t.Fatal(err)
	}
	logger, err := Open(path, redactor)
	if err != nil {
		t.Fatal(err)
	}
	events := bus.New(4)
	cancel := events.SubscribeReliable(logger.FromBus)
	defer cancel()

	// This was below the bus limit but expanded above the audit limit when each
	// one-byte literal was replaced by the full multi-byte mask.
	if err := events.Publish(bus.Event{Type: bus.EvJobStarted, Message: strings.Repeat("x", 400<<10)}); err != nil {
		t.Fatalf("publish redacted event: %v", err)
	}
	if !logger.Healthy() {
		t.Fatalf("redacted event poisoned audit logger: %v", logger.LastError())
	}
	if err := events.Publish(bus.Event{Type: bus.EvJobFinished, Message: "later"}); err != nil {
		t.Fatalf("publish after large redacted event: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := VerifyAll(path); err != nil || n != 2 {
		t.Fatalf("VerifyAll = (%d, %v), want (2, nil)", n, err)
	}
}

func TestRotatedSegmentRetentionUsesVerifiableCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	logger, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger.SetMaxBytes(350)
	logger.SetMaxSegments(2)
	for i := 0; i < 60; i++ {
		if err := logger.Append(Record{Type: "event", Message: fmt.Sprintf("retained-record-%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	segments, err := sealedSegments(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) > 2 {
		t.Fatalf("retained %d sealed segments, want at most 2", len(segments))
	}
	checkpoint, ok, err := loadArchiveCheckpoint(path)
	if err != nil || !ok {
		t.Fatalf("loadArchiveCheckpoint = (%+v, %v, %v)", checkpoint, ok, err)
	}
	if checkpoint.ArchivedThroughSeq <= 0 || checkpoint.ArchivedSegmentCount <= 0 {
		t.Fatalf("checkpoint does not cover pruned prefix: %+v", checkpoint)
	}
	if n, err := VerifyAll(path); err != nil || n != 60 {
		t.Fatalf("VerifyAll retained chain = (%d, %v), want (60, nil)", n, err)
	}

	firstRoot := checkpoint.ArchiveRoot
	firstArchived := checkpoint.ArchivedSegmentCount
	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	reopened.SetMaxBytes(350)
	reopened.SetMaxSegments(2)
	for i := 60; i < 90; i++ {
		if err := reopened.Append(Record{Type: "event", Message: fmt.Sprintf("retained-record-%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint, ok, err = loadArchiveCheckpoint(path)
	if err != nil || !ok {
		t.Fatalf("reload checkpoint = (%+v, %v, %v)", checkpoint, ok, err)
	}
	if checkpoint.ArchiveRoot == firstRoot || checkpoint.ArchivedSegmentCount <= firstArchived {
		t.Fatalf("archive commitment did not advance: before=(%s,%d) after=(%s,%d)", firstRoot, firstArchived, checkpoint.ArchiveRoot, checkpoint.ArchivedSegmentCount)
	}
	segments, err = sealedSegments(path)
	if err != nil || len(segments) > 2 {
		t.Fatalf("sealedSegments after reopen = (%d, %v), want at most 2", len(segments), err)
	}
	if n, err := VerifyAll(path); err != nil || n != 90 {
		t.Fatalf("VerifyAll after reopen = (%d, %v), want (90, nil)", n, err)
	}
}

func TestVerifyAllRejectsTamperedArchiveCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	logger, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger.SetMaxBytes(350)
	logger.SetMaxSegments(1)
	for i := 0; i < 30; i++ {
		if err := logger.Append(Record{Type: "event", Message: "long enough to rotate"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(checkpointPath(path))
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint map[string]any
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint["archive_root"] = strings.Repeat("0", 64)
	raw, err = json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpointPath(path), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAll(path); err == nil {
		t.Fatal("VerifyAll accepted a tampered archive checkpoint")
	}
	if reopened, err := Open(path, nil); err == nil {
		_ = reopened.Close()
		t.Fatal("Open accepted a tampered archive checkpoint")
	}
}

func TestTailCrossesRotatedSegments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.SetMaxBytes(350)
	for i := 0; i < 12; i++ {
		if err := l.Append(Record{Type: "event", Message: fmt.Sprintf("record-%02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := l.Tail(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 7 || records[0].Message != "record-05" || records[6].Message != "record-11" {
		t.Fatalf("rotated tail = %+v", records)
	}
}

func TestTailHasAggregateByteBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 80; i++ {
		if err := l.Append(Record{Type: "event", Message: strings.Repeat("x", 60<<10)}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := l.Tail(80)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) >= 80 || len(encoded) > maxAuditTailBytes+(64<<10) {
		t.Fatalf("tail returned %d records / %d bytes", len(records), len(encoded))
	}
}
