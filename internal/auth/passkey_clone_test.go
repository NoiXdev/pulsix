package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/noixdev/pulsix/internal/auth"
)

// buildCOSEPublicKey CBOR-encodes an EC2/ES256 COSE public key from a real
// ECDSA P-256 key pair, in the shape go-webauthn's webauthncose.ParsePublicKey
// expects (kty=2 EC2, alg=-7 ES256, crv=1 P-256, x/y coordinates).
func buildCOSEPublicKey(t *testing.T, pub *ecdsa.PublicKey) []byte {
	t.Helper()
	// pub.Bytes() is the uncompressed SEC1 encoding (0x04 || X || Y, 32
	// bytes each for P-256); ecdsa.PublicKey.X/Y are deprecated as of Go
	// 1.26 in favor of this accessor.
	raw, err := pub.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 65 || raw[0] != 0x04 {
		t.Fatalf("unexpected P-256 public key encoding, len=%d prefix=%x", len(raw), raw[:1])
	}
	x, y := raw[1:33], raw[33:65]
	b, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// buildSignedAssertionResponse builds a genuinely, cryptographically signed
// WebAuthn login assertion: unlike makeAssertionResponse (which produces a
// structurally valid but unsigned body, sufficient only to reach
// FinishLogin's handler closure before signature verification), this
// signs the standard WebAuthn payload — authenticatorData || sha256(clientDataJSON),
// hashed again with SHA-256 and ECDSA-signed — with a real private key, so
// it passes go-webauthn's actual signature check in
// ValidatePasskeyLogin/validateLogin. This is possible without a Task 15
// virtual authenticator only because the test itself generates the key
// pair and therefore knows the private key, playing the authenticator's
// role directly; the credential's stored public key
// (buildCOSEPublicKey(t, &priv.PublicKey)) must be exactly what a real
// authenticator would have reported at registration.
func buildSignedAssertionResponse(t *testing.T, opts json.RawMessage, rpID, origin string, credID, userHandle []byte, signCount uint32, priv *ecdsa.PrivateKey) json.RawMessage {
	t.Helper()

	var parsedOpts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(opts, &parsedOpts); err != nil {
		t.Fatal(err)
	}

	rpIDHash := sha256.Sum256([]byte(rpID))
	authData := make([]byte, 0, 37)
	authData = append(authData, rpIDHash[:]...)
	authData = append(authData, 0x05) // flags: UP(0x01) | UV(0x04), no attested/extension data.
	counter := make([]byte, 4)
	binary.BigEndian.PutUint32(counter, signCount)
	authData = append(authData, counter...)

	clientData, err := json.Marshal(map[string]any{
		"type": "webauthn.get", "challenge": parsedOpts.PublicKey.Challenge, "origin": origin, "crossOrigin": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	clientDataHash := sha256.Sum256(clientData)

	// The signed payload is authData || sha256(clientDataJSON); go-webauthn
	// then hashes *that* with the credential's registered algorithm (SHA-256
	// for ES256) before verifying the ECDSA signature (ASN.1 DER-encoded, as
	// crypto/ecdsa.SignASN1 produces).
	sigData := make([]byte, 0, len(authData)+len(clientDataHash))
	sigData = append(sigData, authData...)
	sigData = append(sigData, clientDataHash[:]...)
	digest := sha256.Sum256(sigData)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"id": base64.RawURLEncoding.EncodeToString(credID), "rawId": base64.RawURLEncoding.EncodeToString(credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
			"userHandle":        base64.RawURLEncoding.EncodeToString(userHandle),
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestFinishLoginSucceedsAndAdvancesCounterWithRealSignature is the
// baseline this file's clone-signal test contrasts against: a genuine,
// correctly-signed assertion whose counter advances past the stored value
// must succeed and persist the new counter. Without this, a bug that made
// FinishLogin reject every clone-checked login (not just cloned ones) could
// slip through unnoticed.
func TestFinishLoginSucceedsAndAdvancesCounterWithRealSignature(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)
	store.findUserID = &uid

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credID := []byte("advancing-counter-credential-id")
	const storedCounter = uint32(5)
	store.listResult = []auth.Credential{{CredentialID: credID, PublicKey: buildCOSEPublicKey(t, &priv.PublicKey), SignCount: storedCounter}}

	opts, cid, err := pk.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response := buildSignedAssertionResponse(t, opts, testRPID, testOrigin, credID, uid[:], storedCounter+1, priv)

	cookie, err := pk.FinishLogin(context.Background(), cid, response)
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if cookie == "" {
		t.Fatal("FinishLogin succeeded but returned no session cookie")
	}
	if len(store.updateCounterCalls) != 1 || store.updateCounterCalls[0].counter != storedCounter+1 {
		t.Fatalf("updateCounterCalls = %+v, want one call advancing to %d", store.updateCounterCalls, storedCounter+1)
	}
}

// TestFinishLoginRejectsClonedAuthenticatorAndLeavesCounterUnchanged is
// Finding 1 (fix round 1): go-webauthn's ValidatePasskeyLogin does not fail
// a login when the signature counter regresses or repeats — Authenticator.UpdateCounter
// just sets CloneWarning and leaves SignCount unchanged, and validation
// still reports success. This drives a login with a genuine, correctly
// signed assertion (so cryptographic verification passes) whose reported
// counter equals the stored one — exactly what a cloned or rolled-back
// authenticator produces — and asserts both halves of the fix: no session
// is created, and the stored counter is left exactly as it was (no
// UpdateCredentialCounter call at all), not overwritten with the
// unchanged-but-still-written value a naive fix might produce.
func TestFinishLoginRejectsClonedAuthenticatorAndLeavesCounterUnchanged(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)
	store.findUserID = &uid

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credID := []byte("cloned-authenticator-credential-id")
	const storedCounter = uint32(5)
	store.listResult = []auth.Credential{{CredentialID: credID, PublicKey: buildCOSEPublicKey(t, &priv.PublicKey), SignCount: storedCounter}}

	opts, cid, err := pk.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// signCount == storedCounter: a real, validly signed assertion whose
	// counter did not advance — go-webauthn's UpdateCounter sets
	// CloneWarning for exactly this case (authDataCount <= a.SignCount).
	response := buildSignedAssertionResponse(t, opts, testRPID, testOrigin, credID, uid[:], storedCounter, priv)

	cookie, err := pk.FinishLogin(context.Background(), cid, response)
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("FinishLogin err = %v, want ErrInvalidCredentials", err)
	}
	if cookie != "" {
		t.Fatalf("FinishLogin returned a session cookie for a cloned authenticator: %q", cookie)
	}
	if len(store.updateCounterCalls) != 0 {
		t.Fatalf("UpdateCredentialCounter called %+v, want no calls — the stored counter must be left exactly as it was", store.updateCounterCalls)
	}
}
