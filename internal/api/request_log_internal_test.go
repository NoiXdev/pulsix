package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	out := buf.String()
	if !strings.Contains(out, "/invite/[redacted]") {
		t.Errorf("log %q does not carry the redacted path", out)
	}
	if strings.Contains(out, "tok_secret_123") {
		t.Errorf("log %q leaks the invitation token", out)
	}
}
