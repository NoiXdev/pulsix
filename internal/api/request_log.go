package api

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

// redactedInviteURI is what the request log shows for the SPA path of an
// invitation link made before the token moved into the URL fragment.
const redactedInviteURI = "/invite/[redacted]"

// redactRequestURI hides the token of an old-style invitation link
// (/invite/{token}) from the request log. The SPA fallback serves its shell
// for every spelling the client-side router resolves to that route, so the
// decision is made on the decoded, cleaned path with the first segment
// compared case-insensitively: /INVITE/x, //invite/x, /%69nvite/x and
// /invite/./x are as secret as /invite/x. A match replaces the whole URI,
// query string included, since a token could ride there too. Every other URI
// is returned byte for byte: the API carries invitation tokens in a body and
// invitation ids in paths, neither of which is secret.
func redactRequestURI(uri string) string {
	if isInviteTokenPath(requestPath(uri)) {
		return redactedInviteURI
	}
	return uri
}

// requestPath extracts the decoded path from a request URI (origin form, or
// absolute form as a proxy sends it). A URI that does not parse is judged on
// its raw path, so a malformed spelling cannot dodge the redaction.
func requestPath(uri string) string {
	if u, err := url.ParseRequestURI(uri); err == nil {
		return u.Path
	}
	raw, _, _ := strings.Cut(uri, "?")
	return raw
}

// isInviteTokenPath reports whether p is an invitation link: its cleaned form
// is /invite/{something}, or any of its own segments (empty and "." dropped,
// ".." kept, nothing resolved) is "invite" followed by at least one more. The
// second test covers /invite/{token}/.. and /a/../invite/{token}/.., which
// clean to /invite yet still carry the token in the request line. No API
// route has a segment that is exactly "invite" (they are "invites").
func isInviteTokenPath(p string) bool {
	seg, rest, ok := strings.Cut(strings.TrimPrefix(path.Clean("/"+p), "/"), "/")
	if ok && rest != "" && strings.EqualFold(seg, "invite") {
		return true
	}
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			segs = append(segs, s)
		}
	}
	for i := 0; i+1 < len(segs); i++ {
		if strings.EqualFold(segs[i], "invite") {
			return true
		}
	}
	return false
}

// redactingFormatter hands the wrapped formatter a copy of the request whose
// URI carries no invitation token; the request the handlers see is untouched.
type redactingFormatter struct {
	inner middleware.LogFormatter
}

func (f redactingFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	c := r.Clone(r.Context())
	c.RequestURI = redactRequestURI(r.RequestURI)
	if r.URL != nil {
		u := *r.URL
		if isInviteTokenPath(u.Path) {
			u.Path, u.RawPath, u.RawQuery = redactedInviteURI, "", ""
		}
		c.URL = &u
	}
	return f.inner.NewLogEntry(c)
}

// requestLogger is chi's default request logger writing to out, minus
// invitation tokens (backlog 7.80).
func requestLogger(out io.Writer) func(http.Handler) http.Handler {
	return middleware.RequestLogger(redactingFormatter{
		inner: &middleware.DefaultLogFormatter{Logger: log.New(out, "", log.LstdFlags)},
	})
}
