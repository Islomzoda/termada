package output

import "bytes"

// Cleaner turns a raw PTY byte stream into the "cleaned-for-agent"
// representation (spec OUT-3): it strips ANSI/VT escape sequences and collapses
// carriage-return progress-bar redraws, while carrying partial escape sequences
// and a partial final line across chunk boundaries so streaming reads stay
// correct.
//
// One Cleaner instance is stateful and belongs to exactly one job's output
// stream; it must not be shared.
//
// This is deliberately a pragmatic VT filter, not a full terminal emulator. It
// handles CSI (ESC [ … final), OSC (ESC ] … BEL/ST) and simple two-byte escape
// sequences, and resolves carriage returns within a line. Full alt-screen
// applications (vim/htop) are out of scope for the text representation and are
// surfaced via the live dashboard stream instead.
type Cleaner struct {
	escCarry  []byte // a partial escape sequence awaiting more bytes
	lineCarry []byte // the in-progress final line (after CR resolution), not yet terminated
}

const escByte = 0x1b

// Clean consumes p and returns the cleaned text that is now complete. Bytes that
// belong to an unterminated escape sequence or to the still-open final line are
// retained internally and emitted on a later call or by Flush.
func (c *Cleaner) Clean(p []byte) []byte {
	// First, strip escape sequences, joining any carried partial sequence.
	stripped, carry := stripEscapes(append(c.escCarry, p...))
	c.escCarry = carry

	// Resolve carriage returns line by line. A CR returns the cursor to the start
	// of the current line; following text overwrites it. We keep only the final
	// content of each line.
	work := append(c.lineCarry, stripped...)
	c.lineCarry = nil

	var out bytes.Buffer
	start := 0
	for i := 0; i < len(work); i++ {
		if work[i] == '\n' {
			line := resolveCR(work[start:i])
			out.Write(line)
			out.WriteByte('\n')
			start = i + 1
		}
	}
	// The remainder is the still-open final line; resolve CRs within it so the
	// agent sees the latest progress-bar frame, but keep it carried (not yet
	// newline-terminated).
	c.lineCarry = resolveCR(work[start:])
	return out.Bytes()
}

// Flush returns any buffered, not-yet-newline-terminated content. Call it when
// the job has finished and the stream is at EOF.
func (c *Cleaner) Flush() []byte {
	out := c.lineCarry
	c.lineCarry = nil
	// A dangling partial escape sequence at EOF is discarded (it was incomplete
	// and cannot be rendered).
	c.escCarry = nil
	return out
}

// resolveCR collapses carriage returns within a single line (no newlines
// present): everything before the last CR is overwritten.
func resolveCR(line []byte) []byte {
	if idx := bytes.LastIndexByte(line, '\r'); idx >= 0 {
		return line[idx+1:]
	}
	return line
}

// stripEscapes removes ANSI/VT escape sequences from p. It returns the cleaned
// bytes and any trailing partial escape sequence that needs more input.
func stripEscapes(p []byte) (out []byte, carry []byte) {
	out = make([]byte, 0, len(p))
	i := 0
	for i < len(p) {
		b := p[i]
		if b != escByte {
			// Drop other C0 control bytes that are not meaningful as text, but
			// keep CR, LF and TAB.
			if b < 0x20 && b != '\r' && b != '\n' && b != '\t' {
				i++
				continue
			}
			out = append(out, b)
			i++
			continue
		}
		// b == ESC: try to consume a full escape sequence.
		consumed, ok := escLen(p[i:])
		if !ok {
			// Incomplete escape sequence at the end of the buffer; carry it.
			return out, p[i:]
		}
		i += consumed
	}
	return out, nil
}

// escLen reports the length of the escape sequence starting at s[0] (== ESC).
// ok is false when the sequence is incomplete and more bytes are needed.
func escLen(s []byte) (int, bool) {
	if len(s) < 2 {
		return 0, false
	}
	switch s[1] {
	case '[': // CSI: ESC [ params... final(0x40-0x7e)
		for j := 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1, true
			}
		}
		return 0, false
	case ']': // OSC: ESC ] ... terminated by BEL or ST (ESC \)
		for j := 2; j < len(s); j++ {
			if s[j] == 0x07 { // BEL
				return j + 1, true
			}
			if s[j] == escByte && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, true
			}
			if s[j] == escByte && j+1 >= len(s) {
				return 0, false // partial ST
			}
		}
		return 0, false
	default:
		// Two-byte escape (e.g. ESC c, ESC =). Treat ESC + one byte as consumed.
		return 2, true
	}
}
