package api

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
)

func TestRedactRequestURI(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/invite/tok_abc", "/invite/[redacted]"},
		{"/invite/tok_abc?x=1", "/invite/[redacted]"},
		{"/invite", "/invite"},
		{"/invite?x=1", "/invite?x=1"},
		{"/api/v1/invites/accept", "/api/v1/invites/accept"},
		{"/api/v1/orgs/acme/invites/0190b5d2-0000-7000-8000-000000000000", "/api/v1/orgs/acme/invites/0190b5d2-0000-7000-8000-000000000000"},
		{"/o/acme/hosts", "/o/acme/hosts"},
		{"/invitex/abc", "/invitex/abc"},
		{"/invitex/tok", "/invitex/tok"},
		{"/INVITE/tok", "/invite/[redacted]"},
		{"//invite/tok", "/invite/[redacted]"},
		{"/%69nvite/tok", "/invite/[redacted]"},
		{"/invite/./tok", "/invite/[redacted]"},
		{"/invite//tok", "/invite/[redacted]"},
		{"/Invite/tok?x=1", "/invite/[redacted]"},
		{"http://pulsix.test/invite/tok", "/invite/[redacted]"},
		{"/invite/", "/invite/"},
		{"/invite/tok/..", "/invite/[redacted]"},
		{"/invite/tok/../", "/invite/[redacted]"},
		{"/invite/tok/..?x=1", "/invite/[redacted]"},
		{"/a/../invite/tok", "/invite/[redacted]"},
		{"/invite/..", "/invite/[redacted]"},
		{"/invite/./", "/invite/./"},
		{"/a/../invite/tok/..", "/invite/[redacted]"},
		{"/../invite/tok/..", "/invite/[redacted]"},
		{"/x/y/../../invite/tok/..", "/invite/[redacted]"},
		{"/api/v1/orgs/acme/invites/x", "/api/v1/orgs/acme/invites/x"},
	}
	for _, tt := range tests {
		if got := redactRequestURI(tt.in); got != tt.want {
			t.Errorf("redactRequestURI(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRequestLoggerNeverRecordsAnInviteToken(t *testing.T) {
	var buf bytes.Buffer
	var seenPath string
	h := requestLogger(&buf)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/invite/tok_secret_123?a=b", nil))

	if seenPath != "/invite/tok_secret_123" {
		t.Errorf("handler saw path %q, want the original", seenPath)
	}
	// A trailing dot-dot segment resolves back to /invite, but the token is
	// still in the request line.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/invite/tok_secret_123/..", nil))
	out := buf.String()
	if !strings.Contains(out, "/invite/[redacted]") {
		t.Errorf("log %q does not carry the redacted path", out)
	}
	if strings.Contains(out, "tok_secret_123") {
		t.Errorf("log %q leaks the invitation token", out)
	}
}

// A request that is not an invitation link is logged exactly as chi's own
// logger would, apart from the timestamp and the duration.
func TestRequestLoggerMatchesChiForOtherPaths(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	var ours, chi bytes.Buffer
	serve := func(h http.Handler) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/acme/invites/0190b5d2?x=1", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	serve(requestLogger(&ours)(handler))
	serve(middleware.RequestLogger(&middleware.DefaultLogFormatter{Logger: log.New(&chi, "", 0)})(handler))

	strip := func(s string) string {
		s = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `).ReplaceAllString(s, "")
		return regexp.MustCompile(` in \S+\n?$`).ReplaceAllString(s, "")
	}
	if got, want := strip(ours.String()), strip(chi.String()); got != want || got == "" {
		t.Errorf("log line = %q, want chi's %q", got, want)
	}
}
