package api_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// TestCrossOriginMutationsAreRefused is backlog 7.38. The session cookie
// is SameSite=Lax and nothing else stood between another site on the same
// registrable domain (a sibling subdomain) and a signed-in user's
// state-changing requests: Lax sends the cookie on same-site requests, and
// a body with no Content-Type is parsed as JSON. A browser's request from
// another origin is refused now, by Sec-Fetch-Site or, without it, by an
// Origin that is not the manager's; a request from no browser at all (no
// such headers) still works, as does the manager's own page.
func TestCrossOriginMutationsAreRefused(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("csrf-owner@example.test", "correct horse battery")
	s.SeedOrg("csrf-org", owner)
	c := s.LoginAs(owner)

	post := func(headers map[string]string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPatch, s.HTTP.URL+"/api/v1/orgs/csrf-org", bytes.NewReader([]byte(`{"name":"Renamed"}`)))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		refused bool
	}{
		{"a sibling subdomain", map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://evil.example.test"}, true},
		{"another site", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"an older browser on another origin", map[string]string{"Origin": "https://evil.example.test"}, true},
		{"the manager's own page", map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/json"}, false},
		{"no browser", map[string]string{"Content-Type": "application/json"}, false},
	} {
		got := post(tc.headers)
		if tc.refused && got != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", tc.name, got)
		}
		if !tc.refused && got != http.StatusOK {
			t.Errorf("%s: status %d, want 200", tc.name, got)
		}
	}

	// A read stays a read, wherever it comes from.
	req, _ := http.NewRequest(http.MethodGet, s.HTTP.URL+"/api/v1/orgs/csrf-org", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cross-site GET: status %d, want 200", resp.StatusCode)
	}
}
