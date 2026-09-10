package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

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
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("login/finish rejected an anonymous caller before even looking at the ceremony (%d) — it must reach the service, not requireSession", resp.StatusCode)
	}
}

// TestPasskeyMutationsRequireMFAComplete asserts the other gating choice: a
// session that has not completed a required second factor must not be able
// to register, list or delete passkeys — those mutate/inspect the account's
// second factors.
func TestPasskeyMutationsRequireMFAComplete(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("pending@example.test", "correct horse battery")
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
	if resp.StatusCode != http.StatusUnprocessableEntity && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete with a malformed passkey id: %d", resp.StatusCode)
	}
}
