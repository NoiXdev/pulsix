package api_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// signingPasskey is a passkey stored for an account together with the
// private key the test signs its assertions with, playing the
// authenticator's part.
type signingPasskey struct {
	credID []byte
	priv   *ecdsa.PrivateKey
}

// seedSigningPasskey stores a passkey for uid whose assertions the test can
// sign (a slimmed duplicate of internal/auth's seedLoginKey, which lives in
// an auth_test-package file this package cannot import).
func seedSigningPasskey(t *testing.T, s *apitest.Server, uid uuid.UUID) signingPasskey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := priv.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: raw[1:33], -3: raw[33:65]})
	if err != nil {
		t.Fatal(err)
	}
	k := signingPasskey{credID: []byte("reauth-" + uuid.Must(uuid.NewV7()).String()), priv: priv}
	if _, err := s.DB.Pool().Exec(t.Context(),
		`INSERT INTO passkeys (id, user_id, name, credential_id, public_key, attestation) VALUES ($1, $2, 'signing key', $3, $4, 'none')`,
		uuid.Must(uuid.NewV7()), uid, k.credID, cose); err != nil {
		t.Fatal(err)
	}
	return k
}

// sign answers the assertion request opts with k, presenting userHandle as
// the authenticator's user handle and counter as its signature counter.
func (k signingPasskey) sign(t *testing.T, opts json.RawMessage, origin string, userHandle []byte, counter uint32) json.RawMessage {
	t.Helper()
	var parsed struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &parsed); err != nil {
		t.Fatal(err)
	}
	rpIDHash := sha256.Sum256([]byte("localhost"))
	authData := append(append([]byte{}, rpIDHash[:]...), 0x05) // UP | UV
	authData = binary.BigEndian.AppendUint32(authData, counter)
	clientData, err := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": parsed.PublicKey.Challenge, "origin": origin, "crossOrigin": false})
	if err != nil {
		t.Fatal(err)
	}
	clientDataHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), clientDataHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	b, err := json.Marshal(map[string]any{
		"id": enc(k.credID), "rawId": enc(k.credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": enc(clientData), "authenticatorData": enc(authData),
			"signature": enc(sig), "userHandle": enc(userHandle),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// reauthBegin starts a re-authentication ceremony on c's session.
func reauthBegin(t *testing.T, s *apitest.Server, c *http.Client) (string, json.RawMessage) {
	t.Helper()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/reauth/begin", struct{}{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reauth/begin: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out struct {
		CeremonyID string          `json:"ceremony_id"`
		Options    json.RawMessage `json:"options"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.CeremonyID, out.Options
}

func mfaAtOf(t *testing.T, s *apitest.Server, sid uuid.UUID) time.Time {
	t.Helper()
	var at time.Time
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT mfa_at FROM sessions WHERE id = $1`, sid).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func sessionRowCount(t *testing.T, s *apitest.Server, uid uuid.UUID) int {
	t.Helper()
	var n int
	if err := s.DB.Pool().QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The passkey step-up re-proves the factor of the current session: the
// same session id, mfa_at refreshed, its cookie rotated, no second row —
// unlike the discoverable sign-in ceremony the step-up used before, which
// opened a brand-new session and left the old one alive.
func TestPasskeyStepUpKeepsTheSession(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("stepup-pk@example.test", renewPassword)
	k := seedSigningPasskey(t, s, uid)
	c := s.LoginAs(uid)
	stale(t, s, uid)
	oldCookie := sessionCookieOf(t, s, c)
	sid := sessionIDOfCookie(t, oldCookie)
	staleAt := mfaAtOf(t, s, sid)
	rows := sessionRowCount(t, s, uid)

	cid, opts := reauthBegin(t, s, c)
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/reauth/finish", map[string]any{
		"ceremony_id": cid, "response": k.sign(t, opts, s.HTTP.URL, uid[:], 1),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reauth/finish: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	newCookie := sessionCookieOf(t, s, c)
	if newCookie == oldCookie {
		t.Fatal("the step-up did not rotate the session cookie")
	}
	if got := sessionIDOfCookie(t, newCookie); got != sid {
		t.Fatalf("session id after step-up = %s, want %s", got, sid)
	}
	if at := mfaAtOf(t, s, sid); !at.After(staleAt) || time.Since(at) > time.Minute {
		t.Fatalf("mfa_at = %v (was %v), want refreshed to now", at, staleAt)
	}
	if n := sessionRowCount(t, s, uid); n != rows {
		t.Fatalf("session rows %d -> %d, want no second session", rows, n)
	}
	// The fresh factor opens an account-security action.
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{}); resp.StatusCode != http.StatusOK {
		t.Fatalf("register/begin after the step-up: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestPasskeyRenewReplacesTheSession(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("renew-pk@example.test", renewPassword)
	k := seedSigningPasskey(t, s, uid)
	c := s.LoginAs(uid)
	oldCookie := sessionCookieOf(t, s, c)

	cid, opts := reauthBegin(t, s, c)
	start := time.Now()
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/session/renew/passkey", map[string]any{
		"ceremony_id": cid, "response": k.sign(t, opts, s.HTTP.URL, uid[:], 1),
	})
	newID := assertRenewed(t, s, c, resp, oldCookie, start)
	if n := liveSessionCount(t, s, uid); n != 1 {
		t.Fatalf("live sessions = %d, want 1", n)
	}
	ev := findEvent(t, myActivity(t, s, c), "POST renewSessionWithPasskey")
	if ev.OrgID != nil || ev.ObjectType != "session" || ev.ObjectID != newID.String() {
		t.Fatalf("event = %+v, want an instance-level event on session %s", ev, newID)
	}
}

// Another account's passkey neither steps up nor renews A's session, and
// A's session is left exactly as it was.
func TestPasskeyReauthRefusesAnotherAccountsPasskey(t *testing.T) {
	s := apitest.New(t)
	a := s.SeedUser("reauth-a@example.test", renewPassword)
	b := s.SeedUser("reauth-b@example.test", renewPassword)
	seedSigningPasskey(t, s, a)
	kb := seedSigningPasskey(t, s, b)
	c := s.LoginAs(a)
	stale(t, s, a)
	cookie := sessionCookieOf(t, s, c)
	sid := sessionIDOfCookie(t, cookie)
	staleAt := mfaAtOf(t, s, sid)

	for _, path := range []string{"/api/v1/auth/passkeys/reauth/finish", "/api/v1/auth/session/renew/passkey"} {
		for _, handle := range [][]byte{b[:], a[:]} {
			cid, opts := reauthBegin(t, s, c)
			resp := postJSON(t, c, s.HTTP.URL+path, map[string]any{"ceremony_id": cid, "response": kb.sign(t, opts, s.HTTP.URL, handle, 1)})
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s with B's passkey: status %d, want 401", path, resp.StatusCode)
			}
		}
	}
	if got := sessionCookieOf(t, s, c); got != cookie {
		t.Fatal("A's cookie changed")
	}
	if at := mfaAtOf(t, s, sid); !at.Equal(staleAt) {
		t.Fatalf("A's mfa_at %v -> %v, want unchanged", staleAt, at)
	}
	if n := liveSessionCount(t, s, a); n != 1 {
		t.Fatalf("A's live sessions = %d, want 1", n)
	}
}

// The forced password change needs a fresh factor too, so its passkey
// step-up must stay reachable while the account must change its password —
// as /auth/totp/verify is. The renewal is not: such a session changes its
// password first.
func TestPasskeyStepUpReachableWhileThePasswordMustChange(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("stepup-mustchange@example.test", renewPassword)
	k := seedSigningPasskey(t, s, uid)
	c := s.LoginAs(uid)
	markMustChange(t, s, uid)
	stale(t, s, uid)

	cid, opts := reauthBegin(t, s, c)
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/reauth/finish", map[string]any{
		"ceremony_id": cid, "response": k.sign(t, opts, s.HTTP.URL, uid[:], 1),
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reauth/finish while the password must change: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	cid, opts = reauthBegin(t, s, c)
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/session/renew/passkey", map[string]any{
		"ceremony_id": cid, "response": k.sign(t, opts, s.HTTP.URL, uid[:], 2),
	})
	if resp.StatusCode != http.StatusForbidden || problemDetail(t, resp) != passwordChangeRequired {
		t.Fatalf("renew/passkey while the password must change: status %d, want 403 %s", resp.StatusCode, passwordChangeRequired)
	}
}

func TestPasskeyReauthRequestErrors(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("reauth-errors@example.test", renewPassword)
	c := s.LoginAs(uid)

	// No passkey: nothing to offer.
	if resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/reauth/begin", struct{}{}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("reauth/begin without a passkey: status %d, want 404", resp.StatusCode)
	}
	for _, path := range []string{"/api/v1/auth/passkeys/reauth/finish", "/api/v1/auth/session/renew/passkey"} {
		if resp := postJSON(t, c, s.HTTP.URL+path, map[string]any{"ceremony_id": "not-a-uuid", "response": map[string]any{}}); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s with a malformed ceremony id: status %d, want 400", path, resp.StatusCode)
		}
		if resp := postJSON(t, c, s.HTTP.URL+path, map[string]any{"ceremony_id": uuid.Must(uuid.NewV7()).String(), "response": map[string]any{}}); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s with an unknown ceremony: status %d, want 404", path, resp.StatusCode)
		}
	}
	if n := liveSessionCount(t, s, uid); n != 1 {
		t.Fatalf("live sessions = %d, want 1", n)
	}
}
