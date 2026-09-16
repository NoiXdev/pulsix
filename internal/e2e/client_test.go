// The scenario's API client: an authenticated cookie-jar client plus typed
// helpers for every route the scenario drives. All JSON, status-checked —
// a 4xx/5xx fails the scenario right there instead of surfacing later as a
// nil id.
package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
)

// apiClient drives the manager's HTTP API as the enrolled admin.
type apiClient struct {
	t    *testing.T
	base string
	http *http.Client
}

// newAPIClient returns a client with a cookie jar.
func newAPIClient(t *testing.T, base string) *apiClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &apiClient{t: t, base: base, http: &http.Client{Jar: jar}}
}

// do performs one JSON round trip and asserts a 2xx status.
func (a *apiClient) do(method, path string, body, out any) {
	a.t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatalf("encode %s %s body: %v", method, path, err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.base+path, reqBody)
	if err != nil {
		a.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		a.t.Fatalf("%s %s: status %d, body %s", method, path, resp.StatusCode, raw)
	}
	if out == nil {
		return
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		a.t.Fatalf("%s %s: decode response: %v", method, path, err)
	}
}

// sessionCookie returns the current session cookie as a Cookie header
// value — the SSE read below is a manual request, so it needs the header.
func (a *apiClient) sessionCookie() (string, bool) {
	u, err := url.Parse(a.base)
	if err != nil {
		a.t.Fatal(err)
	}
	for _, c := range a.http.Jar.Cookies(u) {
		if c.Name == "pulsix_session" {
			return c.Name + "=" + c.Value, true
		}
	}
	return "", false
}
