// Package output implements the cursor-addressed output buffers, VT cleaning
// and best-effort redaction described in spec §11/§11a/§3a.
package output

import (
	"strconv"
	"sync"

	"github.com/termada/termada/internal/errs"
)

// Buffer is an append-only, cursor-addressable byte stream with a bounded
// retention window. Reads are by absolute byte offset (the cursor); once the
// total written exceeds the retention cap the oldest bytes are dropped and a
// read of an offset that predates the retained window is reported as a gap
// (spec §11a: cursor_expired + gap marker).
//
// Offsets are monotonic and stable for the lifetime of the buffer, which is the
// property the agent-facing cursor relies on.
type Buffer struct {
	mu        sync.Mutex
	data      []byte
	discarded int64 // count of bytes dropped from the front
	cap       int   // max retained bytes; 0 == unbounded
	closed    bool
}

// NewBuffer returns a buffer that retains at most capBytes bytes (0 == unbounded).
func NewBuffer(capBytes int) *Buffer {
	return &Buffer{cap: capBytes}
}

// Write appends p to the buffer, enforcing the retention cap by dropping the
// oldest bytes. It always reports len(p) written and never errors, so it
// satisfies io.Writer for convenience.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if b.cap > 0 && len(b.data) > b.cap {
		drop := len(b.data) - b.cap
		b.discarded += int64(drop)
		b.data = b.data[drop:]
		// Reclaim the underlying array so it does not grow unbounded.
		b.data = append([]byte(nil), b.data...)
	}
	return len(p), nil
}

// Total returns the absolute number of bytes ever written (the next offset).
func (b *Buffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.discarded + int64(len(b.data))
}

// Earliest returns the smallest offset still retained.
func (b *Buffer) Earliest() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.discarded
}

// ReadFrom returns the bytes from the given absolute offset to the current end,
// the next offset to read from, and whether a gap was encountered (the
// requested offset predated the retained window). A negative offset is treated
// as 0.
func (b *Buffer) ReadFrom(offset int64) (chunk []byte, next int64, gap bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := b.discarded + int64(len(b.data))
	if offset < 0 {
		offset = 0
	}
	if offset < b.discarded {
		gap = true
		offset = b.discarded
	}
	if offset > total {
		offset = total
	}
	start := offset - b.discarded
	out := make([]byte, total-offset)
	copy(out, b.data[start:])
	return out, total, gap
}

// Close marks the buffer closed. Reads still work; further writes are ignored at
// the call site (the engine stops writing after a job is finalized).
func (b *Buffer) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

// EncodeCursor renders an offset as the opaque cursor string handed to agents.
func EncodeCursor(offset int64) string {
	return strconv.FormatInt(offset, 10)
}

// DecodeCursor parses a cursor string back to an offset. An empty cursor means
// "from the beginning" (offset 0).
func DecodeCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || v < 0 {
		return 0, errs.New(errs.InvalidArgument, "malformed cursor %q", cursor)
	}
	return v, nil
}
