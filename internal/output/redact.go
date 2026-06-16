package output

import (
	"regexp"
	"strings"
)

// Redactor performs best-effort secret redaction on output before it reaches the
// agent, dashboard or audit (spec §3a / OUT-5). It is explicitly best-effort:
// it catches a set of well-known token formats plus any caller-registered exact
// secrets (e.g. vault values), but it does not guarantee that an arbitrary
// secret will be caught. The honest boundary is documented in the threat-model.
type Redactor struct {
	patterns []*regexp.Regexp
	literals []string // exact secrets (e.g. unlocked vault values) to mask
}

const mask = "«REDACTED»"

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
	r := &Redactor{}
	all := append(append([]string{}, builtinPatterns...), extra...)
	for _, p := range all {
		if re, err := regexp.Compile(p); err == nil {
			r.patterns = append(r.patterns, re)
		}
	}
	return r
}

// AddLiteral registers an exact secret string to be masked wherever it appears.
// Used for unlocked vault values so they can never echo back through output.
func (r *Redactor) AddLiteral(secret string) {
	if secret != "" {
		r.literals = append(r.literals, secret)
	}
}

// Redact returns s with all matched secrets replaced by the mask.
func (r *Redactor) Redact(s string) string {
	for _, lit := range r.literals {
		if lit != "" {
			s = strings.ReplaceAll(s, lit, mask)
		}
	}
	for _, re := range r.patterns {
		s = re.ReplaceAllString(s, mask)
	}
	return s
}
