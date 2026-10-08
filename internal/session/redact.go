package session

import (
	"regexp"
	"strings"
)

// Replacements are bare words, not [brackets], so a redacted value stays a valid YAML plain scalar in the frontmatter.
var redactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?s:.*?)-----END [A-Z0-9 ]*PRIVATE KEY-----`), "REDACTED-private-key"},
	// A key cut off by truncation or a partial paste has no END line: drop everything after BEGIN.
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?s:.*)`), "REDACTED-private-key"},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.?[A-Za-z0-9_-]*`), "REDACTED-jwt"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "REDACTED-aws-key"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), "REDACTED-github-token"},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), "REDACTED-github-token"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "REDACTED-api-key"},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), "REDACTED-slack-token"},
	{regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{16,}`), "${1} REDACTED-token"},
	{regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|secret[_-]?key|client[_-]?secret)` +
		`(["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`), "${1}${2}REDACTED"},
}

// suspiciousBlob is a long hex/base64 run within a few characters of a word that names a credential.
var suspiciousBlob = regexp.MustCompile(`(?i)(?:\b|_)(?:key|secret|token|signature|sig|credential|password|passwd|auth|hmac|private)` +
	`[^\n]{0,30}?\b([A-Za-z0-9+_=-]{40,})`)

// Redact replaces secrets with REDACTED-<kind> markers. It over-redacts on purpose: the note is indexed and synced.
func Redact(text string) string {
	for _, r := range redactions {
		text = r.re.ReplaceAllString(text, r.repl)
	}
	return suspiciousBlob.ReplaceAllStringFunc(text, func(match string) string {
		sub := suspiciousBlob.FindStringSubmatch(match)
		blob := sub[1]
		if !looksRandom(blob) {
			return match
		}
		return strings.TrimSuffix(match, blob) + "REDACTED-blob"
	})
}

// looksRandom keeps kebab-case names and words out of the blob rule: a secret mixes digits and letters and has few
// dashes.
func looksRandom(s string) bool {
	var digits, letters, dashes int
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '-':
			dashes++
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			letters++
		}
	}
	return digits > 0 && letters > 0 && dashes*8 <= len(s)
}
