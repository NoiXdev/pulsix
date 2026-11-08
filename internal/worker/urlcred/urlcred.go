// Package urlcred removes credentials from text that may hold URLs. It is
// a leaf: the requirements service and the runner both redact with it, and
// neither may import the other.
package urlcred

import "regexp"

var (
	// schemeUserinfo matches the userinfo of a URL with a scheme. A match
	// holds no white space, which the galaxy line splitter relies on: it
	// cuts a line only at white space, so it never splits a match.
	schemeUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^@/\s?#]+@`)
	// userPassword matches user:password@ anywhere, scp-style addresses
	// (deploy:secret@host:repo) included.
	userPassword = regexp.MustCompile(`[^@/\s:]+:[^@/\s]+@`)
)

// Redact replaces the credentials in s with [redacted].
//
// The userinfo of any URL with a scheme goes, whole: a token can be a URL's
// bare username (https://TOKEN@host/…), so a user without a password is no
// proof of harmlessness. user:password@ goes wherever it stands. A bare
// scp-style user (git@github.com:org/repo.git) names no secret and stays.
//
// Over-redaction is accepted: a diagnostic is never worth a secret, so
// anything shaped like user:password@ goes, mailto:a@b.c included.
func Redact(s string) string {
	s = schemeUserinfo.ReplaceAllString(s, "${1}[redacted]@")
	return userPassword.ReplaceAllString(s, "[redacted]@")
}
