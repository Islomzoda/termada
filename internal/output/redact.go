package output

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Redactor performs best-effort secret redaction on output before it reaches the
// agent, dashboard or audit (spec §3a / OUT-5). It is explicitly best-effort:
// it catches a set of well-known token formats plus any caller-registered exact
// secrets (e.g. vault values), but it does not guarantee that an arbitrary
// secret will be caught. The honest boundary is documented in the threat-model.
type Redactor struct {
	patterns []*regexp.Regexp

	mu                  sync.RWMutex
	literals            []string // exact secrets, longest first to avoid partial masking
	literalSet          map[string]struct{}
	literalPinned       map[string]bool
	literalReservations map[string]int
	literalBytes        int
}

// LiteralReservation keeps an exact secret active until the caller knows
// whether the operation that could expose it was delivered. A reservation is
// resolved exactly once: Commit pins the literal permanently, while Rollback
// removes it only when no pinned or concurrent reservation still needs it.
type LiteralReservation struct {
	redactor *Redactor
	secret   string
	once     sync.Once
}

const mask = "«REDACTED»"

const (
	maxLiteralCount = 4096
	maxLiteralBytes = 4 << 20
	// maxLiteralScanBytes is a hard work budget for one Redact call's exact-
	// literal pass. Without it, an agent could register 4096 secret inputs and
	// make a 512 KiB audit event trigger roughly 2 GiB of synchronous scanning.
	maxLiteralScanBytes = 32 << 20
)

// ErrLiteralCapacity means the bounded exact-secret registry is full. Callers
// that accept new secrets should surface this error rather than assuming the
// value can be redacted later.
var ErrLiteralCapacity = errors.New("redactor literal capacity exceeded")

// builtinPatterns covers common, high-confidence secret formats.
var builtinPatterns = []string{
	`ghp_[A-Za-z0-9]{20,}`,                                          // GitHub PAT
	`gho_[A-Za-z0-9]{20,}`,                                          // GitHub OAuth
	`github_pat_[A-Za-z0-9_]{20,}`,                                  // GitHub fine-grained PAT
	`xox[baprs]-[A-Za-z0-9-]{10,}`,                                  // Slack token
	`AKIA[0-9A-Z]{16}`,                                              // AWS access key id
	`(?i)aws_secret_access_key\s*=\s*\S+`,                           // AWS secret
	`AIza[0-9A-Za-z\-_]{35}`,                                        // Google API key
	`(?i)api[_-]?key\s*[:=]\s*\S+`,                                  // generic api key
	`(?i)authorization:\s*bearer\s+\S+`,                             // bearer header
	`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`, // JWT
	`-----BEGIN [A-Z ]*PRIVATE KEY-----`,                            // PEM private key header
}

// NewRedactor builds a redactor from the builtin patterns plus any extra
// caller-supplied regex patterns. Invalid extra patterns are skipped.
func NewRedactor(extra []string) *Redactor {
	r := &Redactor{
		literalSet:          make(map[string]struct{}),
		literalPinned:       make(map[string]bool),
		literalReservations: make(map[string]int),
	}
	all := append(append([]string{}, builtinPatterns...), extra...)
	for _, p := range all {
		if re, err := regexp.Compile(p); err == nil {
			r.patterns = append(r.patterns, re)
		}
	}
	return r
}

// AddLiteral registers an exact secret string to be masked wherever it appears.
// The registry is deduplicated and bounded so secret-bearing input cannot grow
// daemon memory without limit.
func (r *Redactor) AddLiteral(secret string) error {
	if secret == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.literalSet[secret]; ok {
		r.literalPinned[secret] = true
		return nil
	}
	if err := r.addLiteralLocked(secret); err != nil {
		return err
	}
	r.literalPinned[secret] = true
	return nil
}

// ReserveLiteral registers secret for redaction until the returned reservation
// is committed or rolled back. The caller must resolve every non-nil
// reservation. Concurrent reservations for the same value share one literal
// entry without allowing one rollback to remove another operation's guard.
func (r *Redactor) ReserveLiteral(secret string) (*LiteralReservation, error) {
	if secret == "" {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.literalSet[secret]; !ok {
		if err := r.addLiteralLocked(secret); err != nil {
			return nil, err
		}
	}
	r.literalReservations[secret]++
	return &LiteralReservation{redactor: r, secret: secret}, nil
}

// Commit pins the reserved literal after successful delivery. It is safe to
// call Commit or Rollback more than once; only the first call has an effect.
func (r *LiteralReservation) Commit() {
	r.resolve(true)
}

// Rollback releases a reservation after failed delivery. A literal remains
// active when it was already pinned or another reservation still references it.
func (r *LiteralReservation) Rollback() {
	r.resolve(false)
}

func (r *LiteralReservation) resolve(commit bool) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		if r.redactor != nil {
			r.redactor.resolveLiteralReservation(r.secret, commit)
		}
		r.redactor = nil
		r.secret = ""
	})
}

func (r *Redactor) addLiteralLocked(secret string) error {
	if len(r.literals) >= maxLiteralCount || len(secret) > maxLiteralBytes-r.literalBytes {
		return ErrLiteralCapacity
	}
	r.literalSet[secret] = struct{}{}
	r.literalBytes += len(secret)
	r.literals = append(r.literals, secret)
	// Mask longer overlapping secrets first. Otherwise registering "token" before
	// "token-with-suffix" would leave the suffix visible.
	sort.SliceStable(r.literals, func(i, j int) bool {
		return len(r.literals[i]) > len(r.literals[j])
	})
	return nil
}

func (r *Redactor) resolveLiteralReservation(secret string, commit bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reservations := r.literalReservations[secret]
	if reservations <= 0 {
		return
	}
	if commit {
		r.literalPinned[secret] = true
	}
	if reservations > 1 {
		r.literalReservations[secret] = reservations - 1
		return
	}
	delete(r.literalReservations, secret)
	if r.literalPinned[secret] {
		return
	}
	delete(r.literalSet, secret)
	delete(r.literalPinned, secret)
	r.literalBytes -= len(secret)
	for i, literal := range r.literals {
		if literal == secret {
			copy(r.literals[i:], r.literals[i+1:])
			r.literals[len(r.literals)-1] = ""
			r.literals = r.literals[:len(r.literals)-1]
			break
		}
	}
}

// Redact returns s with all matched secrets replaced by the mask.
func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	literals := append([]string(nil), r.literals...)
	r.mu.RUnlock()
	if len(s) > 0 && len(literals) > maxLiteralScanBytes/len(s) {
		// Never risk partial redaction when exact matching would exceed the
		// bounded work budget. Whole-value masking is O(len(input)), covers all
		// overlaps and remains non-expanding. Normal literal sets keep the exact
		// longest-first behavior below.
		s = redactionMask(s)
	} else {
		for _, lit := range literals {
			if lit != "" {
				s = strings.ReplaceAll(s, lit, redactionMask(lit))
			}
		}
	}
	for _, re := range r.patterns {
		s = re.ReplaceAllStringFunc(s, redactionMask)
	}
	return s
}

// redactionMask never contains more bytes than the value it replaces. This is
// important at bounded protocol/audit boundaries: registering a one-byte
// literal must not amplify an otherwise valid event into a multi-megabyte one.
func redactionMask(match string) string {
	if len(match) >= len(mask) {
		return mask
	}
	return strings.Repeat("*", len(match))
}
