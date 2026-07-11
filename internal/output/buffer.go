// Package output implements the cursor-addressed output buffers, VT cleaning
// and best-effort redaction described in spec §11/§11a/§3a.
package output

import (
	"strconv"
	"sync"
	"unicode/utf8"

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
	data      []byte // backing storage; bounded buffers use it as a circular ring
	start     int    // index of the oldest retained byte in data
	size      int    // number of retained bytes
	discarded int64  // count of bytes dropped from the front
	cap       int    // max retained bytes; 0 == unbounded
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
	if len(p) == 0 {
		return 0, nil
	}
	if b.cap <= 0 {
		b.data = append(b.data, p...)
		b.size = len(b.data)
		return len(p), nil
	}
	if len(p) >= b.cap {
		previousTotal := b.discarded + int64(b.size)
		b.ensureCapacityLocked(b.cap)
		copy(b.data[:b.cap], p[len(p)-b.cap:])
		b.start = 0
		b.size = b.cap
		b.discarded = previousTotal + int64(len(p)-b.cap)
		b.trimLeadingContinuationLocked()
		return len(p), nil
	}
	needed := b.size + len(p)
	if needed > b.cap {
		needed = b.cap
	}
	b.ensureCapacityLocked(needed)
	if drop := b.size + len(p) - b.cap; drop > 0 {
		b.start = (b.start + drop) % len(b.data)
		b.size -= drop
		b.discarded += int64(drop)
		b.trimLeadingContinuationLocked()
	}
	writeAt := (b.start + b.size) % len(b.data)
	first := min(len(p), len(b.data)-writeAt)
	copy(b.data[writeAt:writeAt+first], p[:first])
	copy(b.data, p[first:])
	b.size += len(p)
	return len(p), nil
}

// trimLeadingContinuationLocked ensures retention never begins in the middle
// of a UTF-8 code point. Output is exposed through JSON strings, so retaining a
// suffix of a rune would otherwise turn one character into replacement runes.
func (b *Buffer) trimLeadingContinuationLocked() {
	for b.size > 0 && isUTF8Continuation(b.data[b.start]) {
		b.start = (b.start + 1) % len(b.data)
		b.size--
		b.discarded++
	}
}

func (b *Buffer) ensureCapacityLocked(needed int) {
	if len(b.data) >= needed {
		return
	}
	capacity := len(b.data) * 2
	if capacity < 4096 {
		capacity = 4096
	}
	if capacity < needed {
		capacity = needed
	}
	if capacity > b.cap {
		capacity = b.cap
	}
	data := make([]byte, capacity)
	b.copyLocked(data[:b.size], 0)
	b.data = data
	b.start = 0
}

func (b *Buffer) copyLocked(dst []byte, relative int) {
	if len(dst) == 0 || b.size == 0 {
		return
	}
	readAt := (b.start + relative) % len(b.data)
	first := min(len(dst), len(b.data)-readAt)
	copy(dst[:first], b.data[readAt:readAt+first])
	copy(dst[first:], b.data[:len(dst)-first])
}

// Total returns the absolute number of bytes ever written (the next offset).
func (b *Buffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.discarded + int64(b.size)
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
	chunk, next, gap, _ = b.ReadFromLimit(offset, 0)
	return chunk, next, gap
}

// ReadFromLimit is ReadFrom with a maximum returned page size. Page boundaries
// preserve complete UTF-8 code points so conversion to a JSON string is not
// lossy. Limits smaller than utf8.UTFMax may return up to utf8.UTFMax bytes to
// guarantee progress. hasMore reports retained bytes after next. A non-positive
// limit returns all available bytes.
func (b *Buffer) ReadFromLimit(offset int64, limit int) (chunk []byte, next int64, gap, hasMore bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := b.discarded + int64(b.size)
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
	// Cursors returned by this buffer are rune-aligned. For a caller-supplied
	// offset in the middle of a retained rune, replay that complete rune instead
	// of returning invalid UTF-8. If retention itself begins mid-rune (possible
	// only for data written before the alignment invariant), skip the fragment.
	for offset > b.discarded && offset < total && isUTF8Continuation(b.byteAtLocked(offset-b.discarded)) {
		offset--
		gap = true
	}
	for offset == b.discarded && offset < total && isUTF8Continuation(b.byteAtLocked(0)) {
		offset++
		b.discarded++
		b.start = (b.start + 1) % len(b.data)
		b.size--
		gap = true
	}
	available := total - offset
	n := available
	effectiveLimit := limit
	if effectiveLimit > 0 && effectiveLimit < utf8.UTFMax {
		effectiveLimit = utf8.UTFMax
	}
	if effectiveLimit > 0 && n > int64(effectiveLimit) {
		n = int64(effectiveLimit)
		hasMore = true
	}
	out := make([]byte, int(n))
	if n > 0 {
		b.copyLocked(out, int(offset-b.discarded))
	}
	// When a page limit or a still-open stream cuts a rune, leave that partial
	// suffix unread. A later page (or write completing the rune) starts from its
	// lead byte. Closed buffers may expose a genuinely invalid terminal suffix;
	// JSON will represent that as a replacement rune, but valid text is lossless.
	if len(out) > 0 && (hasMore || !b.closed) {
		if safe := completeUTF8Prefix(out); safe < len(out) {
			out = out[:safe]
			n = int64(safe)
			hasMore = true
		}
	}
	return out, offset + n, gap, hasMore
}

func (b *Buffer) byteAtLocked(relative int64) byte {
	return b.data[(b.start+int(relative))%len(b.data)]
}

func isUTF8Continuation(c byte) bool { return c&0xc0 == 0x80 }

// completeUTF8Prefix trims only a trailing incomplete encoding. Invalid bytes
// elsewhere are considered complete one-byte runes by utf8.FullRune.
func completeUTF8Prefix(p []byte) int {
	if len(p) == 0 {
		return 0
	}
	start := len(p) - 1
	for start > 0 && isUTF8Continuation(p[start]) {
		start--
	}
	if utf8.FullRune(p[start:]) {
		return len(p)
	}
	return start
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
