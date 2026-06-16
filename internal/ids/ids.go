// Package ids generates unique identifiers for sessions, jobs and internal
// stream markers.
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

func randHex(n int) string {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error on the platforms we support; if it
	// somehow does we still return whatever was filled, which is acceptable for a
	// non-security-critical identifier.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// New returns an identifier of the form "<prefix>_<16 hex chars>".
func New(prefix string) string {
	return prefix + "_" + randHex(8)
}

// Marker returns a 24-char hex token used to delimit a job's output in the
// persistent-shell PTY stream. It must be long and random enough that it is
// effectively impossible for normal command output to contain it.
func Marker() string {
	return randHex(12)
}
