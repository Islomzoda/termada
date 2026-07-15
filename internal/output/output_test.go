package output

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestBufferReadFromAndGap(t *testing.T) {
	b := NewBuffer(10)
	if _, err := b.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("ABC")); err != nil { // forces 3 bytes dropped
		t.Fatal(err)
	}
	if got := b.Total(); got != 13 {
		t.Fatalf("Total = %d, want 13", got)
	}
	chunk, next, gap := b.ReadFrom(0)
	if !gap {
		t.Fatalf("expected gap for offset before retained window")
	}
	if string(chunk) != "3456789ABC" {
		t.Fatalf("chunk = %q, want %q", chunk, "3456789ABC")
	}
	if next != 13 {
		t.Fatalf("next = %d, want 13", next)
	}
	chunk, next, gap = b.ReadFrom(13)
	if gap || len(chunk) != 0 || next != 13 {
		t.Fatalf("read at end: chunk=%q next=%d gap=%v", chunk, next, gap)
	}
	chunk, _, gap = b.ReadFrom(5)
	if gap || string(chunk) != "56789ABC" {
		t.Fatalf("read at 5: chunk=%q gap=%v", chunk, gap)
	}
}

func TestBufferReadFromLimitAcrossRingWrap(t *testing.T) {
	b := NewBuffer(10)
	_, _ = b.Write([]byte("0123456789"))
	_, _ = b.Write([]byte("ABCDEF"))
	chunk, next, gap, more := b.ReadFromLimit(0, 4)
	if string(chunk) != "6789" || next != 10 || !gap || !more {
		t.Fatalf("page1 = %q next=%d gap=%v more=%v", chunk, next, gap, more)
	}
	chunk, next, gap, more = b.ReadFromLimit(next, 4)
	if string(chunk) != "ABCD" || next != 14 || gap || !more {
		t.Fatalf("page2 = %q next=%d gap=%v more=%v", chunk, next, gap, more)
	}
	chunk, next, gap, more = b.ReadFromLimit(next, 4)
	if string(chunk) != "EF" || next != 16 || gap || more {
		t.Fatalf("page3 = %q next=%d gap=%v more=%v", chunk, next, gap, more)
	}
}

func TestBufferPagesDoNotSplitUTF8(t *testing.T) {
	b := NewBuffer(64)
	_, _ = b.Write([]byte("abc€xyz"))

	var (
		cursor int64
		joined []byte
	)
	for i := 0; i < 4; i++ {
		chunk, next, _, more := b.ReadFromLimit(cursor, 4)
		if !utf8.Valid(chunk) {
			t.Fatalf("page %d is invalid UTF-8: %x", i, chunk)
		}
		joined = append(joined, chunk...)
		cursor = next
		if !more {
			break
		}
	}
	if got, want := string(joined), "abc€xyz"; got != want {
		t.Fatalf("joined pages = %q, want %q", got, want)
	}
}

func TestBufferWaitsForPartialUTF8Rune(t *testing.T) {
	b := NewBuffer(64)
	euro := []byte("€")
	_, _ = b.Write(euro[:2])
	chunk, next, _, more := b.ReadFromLimit(0, 64)
	if len(chunk) != 0 || next != 0 || !more {
		t.Fatalf("partial rune page = %x next=%d more=%v", chunk, next, more)
	}
	_, _ = b.Write(euro[2:])
	chunk, next, _, more = b.ReadFromLimit(next, 64)
	if string(chunk) != "€" || next != 3 || more {
		t.Fatalf("completed rune page = %q next=%d more=%v", chunk, next, more)
	}
}

func TestBufferRetentionDropsPartialLeadingRune(t *testing.T) {
	b := NewBuffer(4)
	_, _ = b.Write([]byte("€€"))
	chunk, next, gap := b.ReadFrom(0)
	if !gap || !utf8.Valid(chunk) || string(chunk) != "€" || next != 6 {
		t.Fatalf("retained page = %x next=%d gap=%v", chunk, next, gap)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	if c := EncodeCursor(42); c != "42" {
		t.Fatalf("EncodeCursor = %q", c)
	}
	off, err := DecodeCursor("42")
	if err != nil || off != 42 {
		t.Fatalf("DecodeCursor = %d, %v", off, err)
	}
	if off, err := DecodeCursor(""); err != nil || off != 0 {
		t.Fatalf("empty cursor = %d, %v", off, err)
	}
	if _, err := DecodeCursor("nope"); err == nil {
		t.Fatalf("expected error for malformed cursor")
	}
}

func TestCleanerStripsANSI(t *testing.T) {
	c := &Cleaner{}
	got := string(c.Clean([]byte("\x1b[31mred\x1b[0m\n")))
	if got != "red\n" {
		t.Fatalf("Clean = %q, want %q", got, "red\n")
	}
}

func TestCleanerCRCollapse(t *testing.T) {
	c := &Cleaner{}
	got := string(c.Clean([]byte("progress 10%\rprogress 100%\n")))
	if got != "progress 100%\n" {
		t.Fatalf("Clean = %q, want %q", got, "progress 100%\n")
	}
}

func TestCleanerPartialEscapeAcrossChunks(t *testing.T) {
	c := &Cleaner{}
	if got := string(c.Clean([]byte("\x1b["))); got != "" {
		t.Fatalf("partial escape should buffer, got %q", got)
	}
	if got := string(c.Clean([]byte("31mX\n"))); got != "X\n" {
		t.Fatalf("after completion got %q, want %q", got, "X\n")
	}
}

func TestRedactor(t *testing.T) {
	r := NewRedactor(nil)
	out := r.Redact("here is ghp_abcdefghijklmnopqrstuvwxyz token")
	if out == "here is ghp_abcdefghijklmnopqrstuvwxyz token" {
		t.Fatalf("token was not redacted: %q", out)
	}
	if err := r.AddLiteral("s3cr3tval"); err != nil {
		t.Fatal(err)
	}
	if got := r.Redact("x s3cr3tval y"); got == "x s3cr3tval y" {
		t.Fatalf("literal not redacted: %q", got)
	}
}

func TestRedactorLiteralsAreDeduplicatedBoundedAndLongestFirst(t *testing.T) {
	r := NewRedactor(nil)
	if err := r.AddLiteral("secret"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddLiteral("secret-with-suffix"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddLiteral("secret"); err != nil {
		t.Fatal(err)
	}
	if got := r.Redact("secret-with-suffix"); got != mask {
		t.Fatalf("overlapping literal redaction = %q, want %q", got, mask)
	}
	if len(r.literals) != 2 {
		t.Fatalf("literal count = %d, want 2", len(r.literals))
	}
	if err := r.AddLiteral(strings.Repeat("x", maxLiteralBytes+1)); !errors.Is(err, ErrLiteralCapacity) {
		t.Fatalf("oversized literal error = %v, want %v", err, ErrLiteralCapacity)
	}
}

func TestRedactorLiteralReservationRollbackReleasesCapacity(t *testing.T) {
	r := NewRedactor(nil)
	const secret = "transient-reserved-secret"
	reservation, err := r.ReserveLiteral(secret)
	if err != nil {
		t.Fatalf("reserve literal: %v", err)
	}
	if got, want := r.Redact(secret), redactionMask(secret); got != want {
		t.Fatalf("reserved literal redaction = %q, want %q", got, want)
	}
	reservation.Rollback()
	reservation.Rollback()
	if got := r.Redact(secret); got != secret {
		t.Fatalf("rolled-back literal remains active: %q", got)
	}
	if len(r.literals) != 0 || r.literalBytes != 0 {
		t.Fatalf("rollback retained literals=%d bytes=%d", len(r.literals), r.literalBytes)
	}

	capacityReservation, err := r.ReserveLiteral(strings.Repeat("x", maxLiteralBytes))
	if err != nil {
		t.Fatalf("reserve capacity literal: %v", err)
	}
	if _, err := r.ReserveLiteral("next-secret"); !errors.Is(err, ErrLiteralCapacity) {
		t.Fatalf("reserve beyond capacity error = %v, want %v", err, ErrLiteralCapacity)
	}
	capacityReservation.Rollback()
	next, err := r.ReserveLiteral("next-secret")
	if err != nil {
		t.Fatalf("reserve after rollback: %v", err)
	}
	next.Rollback()
}

func TestRedactorLiteralReservationDoesNotRemoveSharedOrPinnedLiteral(t *testing.T) {
	r := NewRedactor(nil)
	const shared = "shared-reserved-secret"
	first, err := r.ReserveLiteral(shared)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.ReserveLiteral(shared)
	if err != nil {
		t.Fatal(err)
	}
	first.Rollback()
	if got, want := r.Redact(shared), redactionMask(shared); got != want {
		t.Fatalf("one rollback removed a concurrent reservation: got %q, want %q", got, want)
	}
	second.Rollback()
	if got := r.Redact(shared); got != shared {
		t.Fatalf("last rollback retained unpinned literal: %q", got)
	}

	const pinned = "already-pinned-secret"
	if err := r.AddLiteral(pinned); err != nil {
		t.Fatal(err)
	}
	reservation, err := r.ReserveLiteral(pinned)
	if err != nil {
		t.Fatal(err)
	}
	reservation.Rollback()
	if got, want := r.Redact(pinned), redactionMask(pinned); got != want {
		t.Fatalf("rollback removed pinned literal: got %q, want %q", got, want)
	}

	const committed = "committed-reserved-secret"
	committedReservation, err := r.ReserveLiteral(committed)
	if err != nil {
		t.Fatal(err)
	}
	committedReservation.Commit()
	committedReservation.Rollback()
	if got, want := r.Redact(committed), redactionMask(committed); got != want {
		t.Fatalf("commit did not pin literal: got %q, want %q", got, want)
	}
}

func TestRedactorConcurrentLiteralReservations(t *testing.T) {
	r := NewRedactor(nil)
	const secret = "concurrently-reserved-secret"
	const workers = 32

	reservations := make(chan *LiteralReservation, workers)
	errs := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reservation, err := r.ReserveLiteral(secret)
			if err != nil {
				errs <- err
				return
			}
			reservations <- reservation
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("reserve literal: %v", err)
	}
	close(reservations)

	var rollback sync.WaitGroup
	for reservation := range reservations {
		rollback.Add(1)
		go func(reservation *LiteralReservation) {
			defer rollback.Done()
			reservation.Rollback()
		}(reservation)
	}
	rollback.Wait()
	if got := r.Redact(secret); got != secret {
		t.Fatalf("concurrent rollbacks retained literal: %q", got)
	}
}

func TestRedactorLiteralWorkBudgetFailsSecure(t *testing.T) {
	r := NewRedactor(nil)
	input := strings.Repeat("z", 32<<10)
	// None of these literals occurs in input. Their only purpose is to put the
	// legacy literals*input loop above its per-call work budget.
	count := maxLiteralScanBytes/len(input) + 1
	for i := 0; i < count; i++ {
		if err := r.AddLiteral(fmt.Sprintf("registered-secret-%04d", i)); err != nil {
			t.Fatalf("AddLiteral(%d): %v", i, err)
		}
	}
	got := r.Redact(input)
	if want := redactionMask(input); got != want {
		t.Fatalf("over-budget redaction returned %q, want fail-secure mask %q", got, want)
	}
	if len(got) > len(input) {
		t.Fatalf("over-budget redaction expanded %d bytes to %d", len(input), len(got))
	}
}

func TestRedactorNeverExpandsInput(t *testing.T) {
	r := NewRedactor([]string{`.`})
	if err := r.AddLiteral("x"); err != nil {
		t.Fatal(err)
	}
	input := strings.Repeat("x", 4096)
	if got := r.Redact(input); len(got) > len(input) {
		t.Fatalf("redaction expanded %d bytes to %d", len(input), len(got))
	}
	if got := r.Redact("x"); got != "*" {
		t.Fatalf("short literal redaction = %q, want one-byte mask", got)
	}
}

func TestRedactorConcurrentAddAndRedact(t *testing.T) {
	r := NewRedactor(nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_ = r.AddLiteral("shared-secret")
				_ = r.Redact("prefix shared-secret suffix")
			}
		}()
	}
	wg.Wait()
	if got := r.Redact("shared-secret"); got != mask {
		t.Fatalf("Redact = %q, want %q", got, mask)
	}
}

func TestCleanerBoundsInfiniteLineWithoutDataLoss(t *testing.T) {
	c := &Cleaner{}
	chunk := strings.Repeat("x", maxLineCarryBytes/2)
	var out strings.Builder
	for i := 0; i < 20; i++ {
		out.Write(c.Clean([]byte(chunk)))
		if len(c.lineCarry) > maxLineCarryBytes {
			t.Fatalf("line carry grew to %d bytes", len(c.lineCarry))
		}
	}
	out.Write(c.Flush())
	if got, want := out.Len(), 20*len(chunk); got != want {
		t.Fatalf("streamed bytes = %d, want %d", got, want)
	}
}

func TestCleanerBoundsUnterminatedEscape(t *testing.T) {
	c := &Cleaner{}
	if got := c.Clean([]byte("\x1b[" + strings.Repeat("1", maxEscCarryBytes+1))); len(got) != 0 {
		t.Fatalf("oversized CSI emitted %q", got)
	}
	if len(c.escCarry) > maxEscCarryBytes {
		t.Fatalf("escape carry grew to %d bytes", len(c.escCarry))
	}
	if c.escDiscard != '[' {
		t.Fatalf("discard mode = %q, want CSI", c.escDiscard)
	}
	if got := string(c.Clean([]byte("mvisible\n"))); got != "visible\n" {
		t.Fatalf("post-terminator output = %q, want %q", got, "visible\\n")
	}
}
