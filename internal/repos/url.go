package repos

import (
	"fmt"
	"strings"
)

// urlCredentialReason is the refusal for a URL that carries a secret. It
// names the right place for one and never echoes the URL: the error is
// returned to the caller and may be logged.
const urlCredentialReason = "invalid url: must not embed a password or access token; attach a git_token or git_key credential to the repository instead"

// ValidateURL judges a repository URL: the form rules (https://, ssh:// or
// scp-style, no leading dash, no white space, bounded length) and, since
// backlog 7.103, that it embeds no credential. Userinfo with a password is
// refused on every form; a bare user is refused on https (it is a token in
// the user slot) and accepted on ssh:// and scp-style URLs, where it names
// the remote account (git@host:path). The error wraps ErrInvalidInput. The
// shared-definition service applies the same rule through URLProblem.
func ValidateURL(raw string) error {
	if reason, _ := urlProblem(raw); reason != "" {
		return fmt.Errorf("%w: %s", ErrInvalidInput, reason)
	}
	return nil
}

// URLProblem is ValidateURL's verdict as a bare reason ("" when the URL is
// acceptable), for a caller that wraps it in its own invalid-input error;
// credentials reports whether the reason is the embedded-credential
// refusal rather than a form rule.
func URLProblem(raw string) (reason string, credentials bool) {
	return urlProblem(raw)
}

func urlProblem(raw string) (reason string, credentials bool) {
	switch {
	case raw == "":
		return "invalid url: must be an https:// URL, an ssh:// URL or an SCP-like git URL", false
	case strings.HasPrefix(raw, "-"):
		// git would parse it as an option (-oProxyCommand=…).
		return "invalid url: must not start with \"-\"", false
	case strings.ContainsAny(raw, " \t\n\r"):
		return "invalid url: must not contain whitespace", false
	case len(raw) > MaxURLLength:
		return fmt.Sprintf("invalid url: exceeds %d characters", MaxURLLength), false
	case !urlPattern.MatchString(raw):
		return "invalid url: must be an https:// URL, an ssh:// URL or an SCP-like git URL", false
	}
	scheme, userinfo, _, _, ok := splitUserinfo(raw)
	if !ok {
		return "", false
	}
	if strings.Contains(userinfo, ":") || scheme == schemeHTTPS {
		return urlCredentialReason, true
	}
	return "", false
}

const (
	schemeHTTPS = "https"
	schemeSSH   = "ssh"
	schemeSCP   = "scp"
)

// splitUserinfo locates the userinfo of a URL: its scheme (https, ssh, or
// scp for the scp-style form), the userinfo text without the "@", and the
// byte range [start, end) it occupies in raw (end is the index of the
// "@"). ok is false when there is none. For the URL forms the authority
// ends at the first "/", "?" or "#" and the userinfo at the authority's
// last "@", as in RFC 3986, so an "@" in the path is never userinfo.
func splitUserinfo(raw string) (scheme, userinfo string, start, end int, ok bool) {
	switch {
	case strings.HasPrefix(raw, "https://"):
		scheme, start = schemeHTTPS, len("https://")
	case strings.HasPrefix(raw, "ssh://"):
		scheme, start = schemeSSH, len("ssh://")
	default:
		// scp-style: user@host:path, the user ends at the first "@" and
		// cannot contain a "/".
		at := strings.Index(raw, "@")
		if at <= 0 || strings.Contains(raw[:at], "/") {
			return "", "", 0, 0, false
		}
		return schemeSCP, raw[:at], 0, at, true
	}
	authority := raw[start:]
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return "", "", 0, 0, false
	}
	return scheme, authority[:at], start, start + at, true
}

// RedactURL is raw with its secret removed, for what an API returns: a row
// stored before backlog 7.103 may still carry credentials, and every viewer
// of the organization (and of every organization a shared definition
// reaches) reads this field. On https the whole userinfo becomes "***"
// (a bare user there is a token); on ssh:// and scp-style URLs the user is
// kept and only a password is dropped. A URL without userinfo comes back
// unchanged. The worker is sent the stored URL, never this value.
func RedactURL(raw string) string {
	scheme, userinfo, start, end, ok := splitUserinfo(raw)
	if !ok {
		return raw
	}
	if scheme == schemeHTTPS {
		return raw[:start] + "***" + raw[end:]
	}
	user, _, hasPassword := strings.Cut(userinfo, ":")
	if !hasPassword {
		return raw
	}
	return raw[:start] + user + raw[end:]
}

// KeepStoredURL maps a submitted URL back to the stored one when it is the
// stored URL as RedactURL showed it (backlog 7.103): a form edits a row
// stored with credentials by sending back the URL it was given, so
// "https://***@host/x" means "the URL stays", not "write ***", and not
// "change it to a URL with a token". Any other submission is returned as
// is; only a submission the credential refusal would catch is mapped, so
// the ssh and scp-style forms, whose redacted URL is a valid URL of its own
// (the user without the password), are written as submitted.
func KeepStoredURL(submitted, stored string) string {
	if _, credentials := urlProblem(submitted); credentials && submitted == RedactURL(stored) {
		return stored
	}
	return submitted
}
