package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptrace"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// TestPasskeyBeginEndpoints covers what is reachable without a real
// authenticator: option generation for both ceremonies, and that finishing
// a login with a ceremony id that was actually begun for registration fails
// (either as 404 "ceremony not found under this kind" or 401, per
// mapPasskeyErr). The full ceremony — an actual attestation/assertion —
// needs a WebAuthn authenticator and is covered by the Playwright virtual
// authenticator test landing in Task 15.
func TestPasskeyBeginEndpoints(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("pk@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register/begin %d", resp.StatusCode)
	}
	var out struct {
		CeremonyID string          `json:"ceremony_id"`
		Options    json.RawMessage `json:"options"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.CeremonyID == "" || !bytes.Contains(out.Options, []byte(`"challenge"`)) {
		t.Fatalf("body %+v", out)
	}

	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/passkeys/login/begin", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login/begin %d", resp.StatusCode)
	}
	var loginOut struct {
		CeremonyID string          `json:"ceremony_id"`
		Options    json.RawMessage `json:"options"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&loginOut)
	if loginOut.CeremonyID == "" || !bytes.Contains(loginOut.Options, []byte(`"challenge"`)) {
		t.Fatalf("login/begin body %+v", loginOut)
	}

	// Finishing login with the *registration* ceremony's id must fail: the
	// kind check in auth.Passkeys.takeCeremony must reject it before ever
	// looking at the (bogus, unauthenticated) response body.
	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/passkeys/login/finish", map[string]any{"ceremony_id": out.CeremonyID, "response": map[string]any{}})
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login/finish with a register ceremony: %d", resp.StatusCode)
	}

	// And the reverse: finishing registration with the *login* ceremony's
	// id must also fail.
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/finish", map[string]any{"ceremony_id": loginOut.CeremonyID, "name": "Key", "response": map[string]any{}})
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("register/finish with a login ceremony: %d", resp.StatusCode)
	}
}

// TestPasskeyLoginEndpointsRequireNoSession asserts the pre-authentication
// gating choice directly: an anonymous client (no cookie at all) can reach
// both login endpoints, which is the entire point of a passwordless,
// discoverable-credential login.
func TestPasskeyLoginEndpointsRequireNoSession(t *testing.T) {
	s := apitest.New(t)
	anon := s.Client()

	resp := postJSON(t, anon, s.HTTP.URL+"/api/v1/auth/passkeys/login/begin", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login/begin without a session: %d", resp.StatusCode)
	}

	resp = postJSON(t, anon, s.HTTP.URL+"/api/v1/auth/passkeys/login/finish", map[string]any{"ceremony_id": "00000000-0000-7000-8000-000000000000", "response": map[string]any{}})
	// A well-formed but unknown ceremony id reaches the service and is
	// rejected there (404, via mapPasskeyErr), never by requireSession
	// (401): asserting the exact status, not merely "not 401", so this test
	// cannot pass on an unrelated 500 (Finding "adjacent", fix round 1).
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("login/finish without a session: %d, want %d — it must reach the service, not requireSession", resp.StatusCode, http.StatusNotFound)
	}
}

// TestPasskeyMutationsRequireMFAComplete asserts the other gating choice: a
// session whose account has a second factor it has not verified yet must
// not be able to register, list or delete passkeys — those mutate/inspect
// the account's second factors. (An account with no factor at all may
// register one: TestEnrolmentSessionCanOnlyEnrol.)
func TestPasskeyMutationsRequireMFAComplete(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("pending@example.test", "correct horse battery")
	s.SeedPasskey(uid, "existing key")
	c := s.LoginAsPending(uid)

	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("register/begin with a pending session: %d, want 403", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, s.HTTP.URL+"/api/v1/auth/passkeys", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("list with a pending session: %d, want 403", resp.StatusCode)
	}
	del, _ := http.NewRequest(http.MethodDelete, s.HTTP.URL+"/api/v1/auth/passkeys/00000000-0000-7000-8000-000000000000", nil)
	resp, err = c.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete with a pending session: %d, want 403", resp.StatusCode)
	}
}

// TestPasskeyListAndDeleteRoundTrip exercises List/Delete against the real
// store via the HTTP layer, without needing a real authenticator: an
// initially empty list, a 404 deleting an id that was never registered, and
// a 401/400 shaped response never mistaken for success.
func TestPasskeyListAndDeleteRoundTrip(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("list@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	req, _ := http.NewRequest(http.MethodGet, s.HTTP.URL+"/api/v1/auth/passkeys", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	var list struct {
		Passkeys []any `json:"passkeys"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Passkeys) != 0 {
		t.Fatalf("list for a fresh user = %+v, want empty", list.Passkeys)
	}

	del, _ := http.NewRequest(http.MethodDelete, s.HTTP.URL+"/api/v1/auth/passkeys/00000000-0000-7000-8000-000000000000", nil)
	resp, err = c.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete of an unknown passkey: %d, want 404", resp.StatusCode)
	}
}

// TestPasskeyEndpointsRejectMalformedCeremonyID covers the input-validation
// edge the service layer never sees: a ceremony_id that is not even a valid
// UUID must 400, not panic or fall through to the service as a zero UUID.
func TestPasskeyEndpointsRejectMalformedCeremonyID(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("badid@example.test", "correct horse battery")
	c := s.LoginAs(uid)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/finish", map[string]any{"ceremony_id": "not-a-uuid", "name": "Key", "response": map[string]any{}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("register/finish with a malformed ceremony id: %d, want 400 (from the handler's own uuid.Parse, not schema validation)", resp.StatusCode)
	}
	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/auth/passkeys/login/finish", map[string]any{"ceremony_id": "not-a-uuid", "response": map[string]any{}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("login/finish with a malformed ceremony id: %d, want 400 (from the handler's own uuid.Parse, not schema validation)", resp.StatusCode)
	}
	del, _ := http.NewRequest(http.MethodDelete, s.HTTP.URL+"/api/v1/auth/passkeys/not-a-uuid", nil)
	resp, err := c.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	// passkeyIDInput.ID carries no format:"uuid" tag (Finding "adjacent", fix
	// round 1), so huma's schema layer never intercepts this: the handler's
	// own uuid.Parse always runs and always produces exactly 400, not a
	// hedge between huma's 422 and the handler's 400.
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete with a malformed passkey id: %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestPasskeyRegisterFinishAfterSecondFactorResetStoresNothing is the
// request an administrator's second-factor reset must win against (backlog
// 7.109). The cookie middleware resolves the session when the request's
// headers arrive; the body is read later. A client that holds back the body
// of register/finish until the reset has committed reached the handler
// with the session it had before the reset, and the passkey it then
// registered survived the reset that was meant to lock it out.
//
// "Expect: 100-continue" makes that window deterministic: the server sends
// 100 Continue only when the handler first reads the body, so by then the
// session has been resolved; the reset runs, and only then is the body sent.
func TestPasskeyRegisterFinishAfterSecondFactorResetStoresNothing(t *testing.T) {
	s := apitest.New(t)
	admin := s.SeedUser("reset-admin@example.test", "correct horse battery")
	uid := s.SeedUser("parked-finish@example.test", "correct horse battery")
	// The account has a factor and the session proved it just now, so
	// register/begin passes the step-up check (backlog 1.28).
	s.SeedPasskey(uid, "Existing key")
	c := s.LoginAs(uid)

	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register/begin: %d", resp.StatusCode)
	}
	var begin struct {
		CeremonyID string          `json:"ceremony_id"`
		Options    json.RawMessage `json:"options"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&begin); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"ceremony_id": begin.CeremonyID, "name": "Planted key",
		"response": buildNoneAttestationResponse(t, begin.Options, "localhost", s.HTTP.URL, []byte("parked-finish-credential")),
	})
	if err != nil {
		t.Fatal(err)
	}

	// The client waits for 100 Continue before it sends the body, and the
	// body itself waits on the pipe until the reset has committed.
	c.Transport.(*http.Transport).ExpectContinueTimeout = time.Minute
	pr, pw := io.Pipe()
	resolved := make(chan struct{})
	trace := &httptrace.ClientTrace{Got100Continue: func() { close(resolved) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace),
		http.MethodPost, s.HTTP.URL+"/api/v1/auth/passkeys/register/finish", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Expect", "100-continue")
	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := c.Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		_ = resp.Body.Close()
		done <- result{status: resp.StatusCode}
	}()

	select {
	case <-resolved:
	case r := <-done:
		t.Fatalf("register/finish answered before it read its body: status %d, err %v", r.status, r.err)
	case <-time.After(30 * time.Second):
		t.Fatal("register/finish never asked for its body")
	}
	if _, err := s.Deps.Accounts.ResetSecondFactor(context.Background(), true, admin, uid); err != nil {
		t.Fatalf("ResetSecondFactor: %v", err)
	}
	if _, err := pw.Write(body); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()

	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.status != http.StatusUnauthorized {
		t.Errorf("register/finish after the reset: %d, want %d", r.status, http.StatusUnauthorized)
	}
	var n int
	if err := s.DB.Pool().QueryRow(context.Background(), `SELECT count(*) FROM passkeys WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("passkeys after the reset = %d, want 0", n)
	}
	var events int
	if err := s.DB.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE action = 'POST finishPasskeyRegistration'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Errorf("a refused registration recorded %d audit events, want 0", events)
	}
}
