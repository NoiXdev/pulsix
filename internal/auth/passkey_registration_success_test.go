package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// buildNoneAttestationResponse constructs a hand-built WebAuthn registration
// response using the "none" attestation format. "none" is the one format
// that carries no signature at all — an authenticator using it simply
// asserts a public key without proving possession of the matching private
// key — so, unlike every other format and unlike a login assertion, it can
// be built by hand and still pass go-webauthn's *real* client-data and
// attestation-object verification (challenge/origin/type matching, CBOR
// decoding, RPID hash, algorithm negotiation) rather than only failing at
// the first parse step the way the other tests in this package do.
//
// This is deliberately the only place in the suite that does this: it
// exists to give FinishRegistration's success path real coverage without
// pretending to verify a signature this test has no authenticator to
// produce. Login (assertion signing) and every attestation format other
// than "none" still need a genuine authenticator, which is Task 15's job
// (the Playwright virtual authenticator).
func buildNoneAttestationResponse(t *testing.T, opts json.RawMessage, rpID, origin string, credID []byte) json.RawMessage {
	t.Helper()

	var parsedOpts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &parsedOpts); err != nil {
		t.Fatal(err)
	}

	// A minimal COSE EC2/ES256 public key: kty=2 (EC2), alg=-7 (ES256). No
	// curve point is needed — "none" attestation never uses this key to
	// verify anything, it is only decoded far enough to read the algorithm.
	pubKeyCBOR, err := cbor.Marshal(map[int]any{1: 2, 3: -7})
	if err != nil {
		t.Fatal(err)
	}

	rpIDHash := sha256.Sum256([]byte(rpID))
	authData := make([]byte, 0, 37+16+2+len(credID)+len(pubKeyCBOR))
	authData = append(authData, rpIDHash[:]...)      // rpIdHash (32 bytes)
	authData = append(authData, 0x45)                // flags: UP(0x01) | UV(0x04) | AT(0x40)
	authData = append(authData, 0, 0, 0, 1)          // signCount = 1
	authData = append(authData, make([]byte, 16)...) // aaguid: all zero, not checked here
	credIDLen := make([]byte, 2)
	binary.BigEndian.PutUint16(credIDLen, uint16(len(credID)))
	authData = append(authData, credIDLen...)
	authData = append(authData, credID...)
	authData = append(authData, pubKeyCBOR...)

	attestationObject, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": authData,
	})
	if err != nil {
		t.Fatal(err)
	}

	clientData, err := json.Marshal(map[string]any{
		"type": "webauthn.create", "challenge": parsedOpts.PublicKey.Challenge, "origin": origin, "crossOrigin": false,
	})
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"id": base64.RawURLEncoding.EncodeToString(credID), "rawId": base64.RawURLEncoding.EncodeToString(credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestationObject),
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestFinishRegistrationSucceedsWithNoneAttestation drives FinishRegistration
// all the way through go-webauthn's real verification to a stored
// credential — the actual success path, not just the error branches every
// other test in this package exercises.
func TestFinishRegistrationSucceedsWithNoneAttestation(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)

	opts, cid, err := pk.BeginRegistration(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	credID := []byte("integration-test-credential-id-0")
	response := buildNoneAttestationResponse(t, opts, testRPID, testOrigin, credID)

	if err := pk.FinishRegistration(context.Background(), uid, cid, "YubiKey", response); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	if len(store.addCalls) != 1 {
		t.Fatalf("AddCredential called %d times, want 1", len(store.addCalls))
	}
	added := store.addCalls[0]
	if added.userID != uid || added.name != "YubiKey" || string(added.cred.CredentialID) != string(credID) {
		t.Fatalf("AddCredential args = %+v", added)
	}
	if added.cred.Attestation != "none" {
		t.Errorf("Attestation = %q, want none", added.cred.Attestation)
	}
}

// TestFinishRegistrationRejectsWrongChallenge confirms the hand-built
// response above is actually being verified, not rubber-stamped: changing
// only the challenge (so it no longer matches the one BeginRegistration
// issued) must make CreateCredential fail, exactly as a replayed or
// tampered registration response would in production.
func TestFinishRegistrationRejectsWrongChallenge(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)

	_, cid, err := pk.BeginRegistration(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	wrongOpts := json.RawMessage(`{"publicKey":{"challenge":"not-the-real-challenge"}}`)
	credID := []byte("integration-test-credential-id-1")
	response := buildNoneAttestationResponse(t, wrongOpts, testRPID, testOrigin, credID)

	if err := pk.FinishRegistration(context.Background(), uid, cid, "YubiKey", response); err == nil {
		t.Fatal("FinishRegistration accepted a response for the wrong challenge")
	}
	if len(store.addCalls) != 0 {
		t.Fatal("AddCredential was called despite verification failing")
	}
}
