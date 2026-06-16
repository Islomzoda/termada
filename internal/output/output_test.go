package output

import "testing"

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
	r.AddLiteral("s3cr3tval")
	if got := r.Redact("x s3cr3tval y"); got == "x s3cr3tval y" {
		t.Fatalf("literal not redacted: %q", got)
	}
}
