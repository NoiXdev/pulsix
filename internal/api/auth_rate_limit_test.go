package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// The per-address rate limit on the four public auth endpoints (backlog
// 1.20). Every test trusts the loopback peer (127.0.0.1) and sends its
// client address as X-Forwarded-For, which is how any caller behind a
// configured reverse proxy is seen — and how the tests distinguish
// addresses on an httptest server, whose callers otherwise all share the
// TCP peer's address.

// loopbackTrusted trusts the test server's own listener address, so an
// X-Forwarded-For header names the client.
func loopbackTrusted() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
}

// postFrom posts body to url with the given X-Forwarded-For header,
// anonymously (no cookie jar).
func postFrom(url, xff string, body any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	return http.DefaultClient.Do(req)
}

// xffLimit is the per-test auth limit: three failed attempts trip it.
const xffLimit = 3

// limitedServer builds a test server with the rate limit on and loopback
// trusted, so the X-Forwarded-For header names the client.
func limitedServer(t *testing.T) *apitest.Server {
	t.Helper()
	return apitest.New(t,
		apitest.WithAuthRateLimit(xffLimit),
		apitest.WithTrustedProxies(loopbackTrusted()),
	)
}

// addressA and addressB are two distinct client addresses a request can
// present through the trusted loopback proxy.
const (
	addressA = "198.51.100.7"
	addressB = "198.51.100.8"
)

// expectTrips drives one endpoint to its 429: the first xffLimit failures
// answer with the endpoint's own error response and are charged, the next
// attempt answers 429 with a Retry-After, and the same attempt from another
// address answers with the endpoint's real (non-429) response. Each call
// uses its own server, so the trip is this endpoint's. hit is one failing
// request from xff; it returns the endpoint's own failure status.
func expectTrips(t *testing.T, s *apitest.Server, hit func(xff string) int) {
	t.Helper()
	for i := 0; i < xffLimit; i++ {
		if got := hit(addressA); got >= http.StatusInternalServerError {
			t.Fatalf("failure %d = %d, want the endpoint's 4xx", i+1, got)
		}
	}
	if last := hit(addressA); last != http.StatusTooManyRequests {
		t.Fatalf("attempt after %d failures = %d, want 429", xffLimit, last)
	}
	if got := hit(addressB); got == http.StatusTooManyRequests {
		t.Fatal("another address must keep its own budget")
	}
}

// retryAfterOf returns the Retry-After header of a 429 response, asserting
// it is present and names a positive number of seconds.
func retryAfterOf(t *testing.T, resp *http.Response) int {
	t.Helper()
	ra, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || ra < 1 {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", resp.Header.Get("Retry-After"))
	}
	return ra
}

// TestAuthRateLimitTripsOnLogin: three wrong passwords pass through as the
// endpoint's own 401 and charge the budget, the next attempt answers 429
// with a Retry-After and a problem-details body in huma's error shape, and
// the same attempt from another address answers 401 as usual.
func TestAuthRateLimitTripsOnLogin(t *testing.T) {
	s := limitedServer(t)
	s.SeedUser("login@example.test", "correct horse battery")
	hit := func(xff string) *http.Response {
		resp, err := postFrom(s.HTTP.URL+"/api/v1/auth/login", xff, map[string]string{"email": "login@example.test", "password": "wrong"})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for i := 0; i < xffLimit; i++ {
		if got := hit(addressA).StatusCode; got != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i+1, got)
		}
	}
	resp := hit(addressA)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt after %d failures = %d, want 429", xffLimit, resp.StatusCode)
	}
	retryAfterOf(t, resp)
	var problem struct {
		Title  string `json:"title"`
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.Status != http.StatusTooManyRequests || problem.Title != "Too Many Requests" || problem.Detail == "" {
		t.Fatalf("429 body = %+v, want huma's error shape", problem)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("429 Content-Type = %q", ct)
	}

	if got := hit(addressB).StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("first failure from another address = %d, want 401", got)
	}
}

// TestAuthRateLimitTripsOnTOTPVerify enrols a TOTP factor first, so a wrong
// code reaches the real verification path (401 "invalid code") rather than
// bouncing off the missing secret.
func TestAuthRateLimitTripsOnTOTPVerify(t *testing.T) {
	s := limitedServer(t)
	uid := s.SeedUser("totp@example.test", "correct horse battery")
	enroller := s.LoginAs(uid)
	resp := postJSON(t, enroller, s.HTTP.URL+"/api/v1/auth/totp/enrol", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("totp/enrol: %d", resp.StatusCode)
	}
	var enrol struct {
		Secret string `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&enrol)
	code, _ := totp.GenerateCode(enrol.Secret, time.Now())
	resp = postJSON(t, enroller, s.HTTP.URL+"/api/v1/auth/totp/confirm", map[string]string{"code": code})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("totp/confirm: %d", resp.StatusCode)
	}

	pending := s.Client()
	resp = postJSON(t, pending, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "totp@example.test", "password": "correct horse battery"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}

	hit := func(xff string) int {
		// The limit keys on the address, not the session: one pending
		// session per address keeps the test focused on the address key.
		resp, err := postFrom(s.HTTP.URL+"/api/v1/auth/totp/verify", xff, map[string]string{"code": "000000"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfterOf(t, resp)
		}
		return resp.StatusCode
	}
	expectTrips(t, s, hit)
}

// TestAuthRateLimitTripsOnPasskeyFinish drives the passkey login finish
// with a well-formed but unknown ceremony id: the endpoint's own 404, the
// same failure a ceremony-id probe gets, is what charges the budget.
func TestAuthRateLimitTripsOnPasskeyFinish(t *testing.T) {
	s := limitedServer(t)
	hit := func(xff string) int {
		resp, err := postFrom(s.HTTP.URL+"/api/v1/auth/passkeys/login/finish", xff,
			map[string]any{"ceremony_id": "00000000-0000-7000-8000-000000000000", "response": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfterOf(t, resp)
		}
		return resp.StatusCode
	}
	expectTrips(t, s, hit)
}

// TestAuthRateLimitTripsOnInviteAccept drives the accept endpoint with a
// token no invite carries: its own 404, charged like the rest.
func TestAuthRateLimitTripsOnInviteAccept(t *testing.T) {
	s := limitedServer(t)
	hit := func(xff string) int {
		resp, err := postFrom(s.HTTP.URL+"/api/v1/invites/accept", xff, map[string]string{"token": "no-such-invite"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfterOf(t, resp)
		}
		return resp.StatusCode
	}
	expectTrips(t, s, hit)
}

// TestAuthRateLimitCountsFailuresOnly: successful attempts are never
// charged, so a caller can sign in repeatedly while failures pile up —
// matching the hook limiter, where verified deliveries do not count.
func TestAuthRateLimitCountsFailuresOnly(t *testing.T) {
	s := apitest.New(t, apitest.WithAuthRateLimit(2), apitest.WithTrustedProxies(loopbackTrusted()))
	s.SeedUser("counted@example.test", "correct horse battery")
	c := s.Client()

	for i := 0; i < 6; i++ {
		resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/login", map[string]string{"email": "counted@example.test", "password": "correct horse battery"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("successful login %d = %d, the limit must never charge a success", i+1, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

// TestAuthRateLimitDisabledWithZero: PULSIX_AUTH_RATE_LIMIT=0 (the apitest
// default) keeps the endpoints unlimited — many failed attempts in a row,
// no 429 anywhere.
func TestAuthRateLimitDisabledWithZero(t *testing.T) {
	s := apitest.New(t, apitest.WithTrustedProxies(loopbackTrusted()))
	s.SeedUser("off@example.test", "correct horse battery")
	for i := 0; i < 25; i++ {
		resp, err := postFrom(s.HTTP.URL+"/api/v1/auth/login", addressA, map[string]string{"email": "off@example.test", "password": "wrong"})
		if err != nil {
			t.Fatal(err)
		}
		body := mustReadBody(t, resp)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d %s, want 401 with the limiter disabled", i+1, resp.StatusCode, body)
		}
	}
}

// TestAuthRateLimitKeysOnTheResolvedAddress: the budget keys on the
// address clientip resolves. Behind a trusted proxy that is the forwarded
// address, so two clients of one proxy keep separate budgets; without a
// trusted proxy the forwarded header is ignored and the TCP peer's budget
// applies to every client, so no caller can dodge or split it by forging
// X-Forwarded-For.
func TestAuthRateLimitKeysOnTheResolvedAddress(t *testing.T) {
	// Trusted loopback: the forwarded address is the client.
	s := apitest.New(t, apitest.WithAuthRateLimit(2), apitest.WithTrustedProxies(loopbackTrusted()))
	s.SeedUser("resolved@example.test", "correct horse battery")
	fail := func(xff string) int {
		resp, err := postFrom(s.HTTP.URL+"/api/v1/auth/login", xff, map[string]string{"email": "resolved@example.test", "password": "wrong"})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	first := fail(addressA)
	second := fail(addressA)
	if first != http.StatusUnauthorized || second != http.StatusUnauthorized {
		t.Fatalf("the first two failures are answered as usual: %d, %d", first, second)
	}
	if got := fail(addressA); got != http.StatusTooManyRequests {
		t.Fatalf("third failure = %d, want 429", got)
	}
	if fail(addressB) != http.StatusUnauthorized {
		t.Fatal("another forwarded address keeps its own budget behind the trusted proxy")
	}

	// No trusted proxy (the apitest default): the same forwarded headers
	// change nothing — every request is the loopback peer, whose budget
	// was spent by the two failures above.
	u := apitest.New(t, apitest.WithAuthRateLimit(2))
	u.SeedUser("unresolved@example.test", "correct horse battery")
	spoof := func(xff string) int {
		resp, err := postFrom(u.HTTP.URL+"/api/v1/auth/login", xff, map[string]string{"email": "unresolved@example.test", "password": "wrong"})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if spoof("203.0.113.1") != http.StatusUnauthorized || spoof("203.0.113.2") != http.StatusUnauthorized {
		t.Fatal("forged forwarded headers must not split an untrusted peer's budget")
	}
	if got := spoof("203.0.113.3"); got != http.StatusTooManyRequests {
		t.Fatalf("third failure from the peer = %d, want 429", got)
	}
}

// TestAuthRateLimitLeavesOtherRoutesAlone: a spent auth budget never
// touches any other route — POST /orgs from the same address still works.
func TestAuthRateLimitLeavesOtherRoutesAlone(t *testing.T) {
	s := apitest.New(t, apitest.WithAuthRateLimit(1), apitest.WithTrustedProxies(loopbackTrusted()))
	uid := s.SeedUser("spill@example.test", "correct horse battery")
	resp, err := postFrom(s.HTTP.URL+"/api/v1/auth/login", addressA, map[string]string{"email": "spill@example.test", "password": "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	resp, err = postFrom(s.HTTP.URL+"/api/v1/auth/login", addressA, map[string]string{"email": "spill@example.test", "password": "wrong"})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second failure = %d, want 429", resp.StatusCode)
	}

	resp = postJSON(t, s.LoginAs(uid), s.HTTP.URL+"/api/v1/orgs", map[string]any{"slug": "still-open", "name": "still-open"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /orgs from the limited address = %d, want 201", resp.StatusCode)
	}
}
