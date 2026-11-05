package api

import (
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

// redactedInviteURI is what the request log shows for the SPA path of an
// invitation link made before the token moved into the URL fragment.
const redactedInviteURI = "/invite/[redacted]"

// redactRequestURI hides the token of an old-style invitation link
// (/invite/{token}) from the request log. A query string is dropped with it,
// since a token could ride there too. Every other URI is returned unchanged:
// the API carries invitation tokens in a body and invitation ids in paths,
// neither of which is secret.
func redactRequestURI(uri string) string {
	rest, ok := strings.CutPrefix(uri, "/invite/")
	if !ok || rest == "" || rest[0] == '?' {
		return uri
	}
	return redactedInviteURI
}

// redactingFormatter hands the wrapped formatter a copy of the request whose
// URI carries no invitation token; the request the handlers see is untouched.
type redactingFormatter struct {
	inner middleware.LogFormatter
}

func (f redactingFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	c := r.Clone(r.Context())
	c.RequestURI = redactRequestURI(r.RequestURI)
	u := *c.URL
	u.Path = redactRequestURI(u.Path)
	if u.Path == redactedInviteURI {
		u.RawPath, u.RawQuery = "", ""
	}
	c.URL = &u
	return f.inner.NewLogEntry(c)
}

// requestLogger is chi's default request logger writing to out, minus
// invitation tokens (backlog 7.80).
func requestLogger(out io.Writer) func(http.Handler) http.Handler {
	return middleware.RequestLogger(redactingFormatter{
		inner: &middleware.DefaultLogFormatter{Logger: log.New(out, "", log.LstdFlags)},
	})
}
