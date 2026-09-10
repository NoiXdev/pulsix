package api_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"github.com/noixdev/pulsix/internal/api/apitest"
)

// buildNoneAttestationResponse is a slimmed-down duplicate of
// internal/auth's helper of the same name (that one lives in an
// auth_test-package file and isn't importable from here): a hand-built
// "none"-format WebAuthn registration response, the one attestation format
// with no signature to fake, so it passes go-webauthn's real client-data
// and attestation-object verification without a Task 15 virtual
// authenticator.
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

	pubKeyCBOR, err := cbor.Marshal(map[int]any{1: 2, 3: -7})
	if err != nil {
		t.Fatal(err)
	}

	rpIDHash := sha256.Sum256([]byte(rpID))
	authData := make([]byte, 0, 37+16+2+len(credID)+len(pubKeyCBOR))
	authData = append(authData, rpIDHash[:]...)
	authData = append(authData, 0x45) // flags: UP | UV | AT
	authData = append(authData, 0, 0, 0, 1)
	authData = append(authData, make([]byte, 16)...) // aaguid
	credIDLen := make([]byte, 2)
	binary.BigEndian.PutUint16(credIDLen, uint16(len(credID)))
	authData = append(authData, credIDLen...)
	authData = append(authData, credID...)
	authData = append(authData, pubKeyCBOR...)

	attestationObject, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
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

// TestPasskeyRegisterFinishRejectsDuplicateCredentialID is the HTTP-level
// counterpart to internal/auth's
// TestSQLPasskeyStoreAddCredentialRejectsDuplicateCredentialID: registering
// the same credential id a second time (here, for the same account — a
// browser retry, or a credential id collision) must answer 409, not the
// unmapped-error 500 a raw unique-violation would otherwise produce through
// mapAuthErr's default branch (Finding "adjacent", fix round 1).
func TestPasskeyRegisterFinishRejectsDuplicateCredentialID(t *testing.T) {
	s := apitest.New(t)
	uid := s.SeedUser("dup-credential@example.test", "correct horse battery")
	c := s.LoginAs(uid)
	credID := []byte("api-level-duplicate-credential-id")

	begin := func() (json.RawMessage, string) {
		resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/begin", struct{}{})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("register/begin: %d", resp.StatusCode)
		}
		var out struct {
			CeremonyID string          `json:"ceremony_id"`
			Options    json.RawMessage `json:"options"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Options, out.CeremonyID
	}

	opts, cid := begin()
	response := buildNoneAttestationResponse(t, opts, "localhost", s.HTTP.URL, credID)
	resp := postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/finish", map[string]any{"ceremony_id": cid, "name": "First", "response": json.RawMessage(response)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first register/finish: %d", resp.StatusCode)
	}

	opts2, cid2 := begin()
	response2 := buildNoneAttestationResponse(t, opts2, "localhost", s.HTTP.URL, credID)
	resp = postJSON(t, c, s.HTTP.URL+"/api/v1/auth/passkeys/register/finish", map[string]any{"ceremony_id": cid2, "name": "Second", "response": json.RawMessage(response2)})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second register/finish (duplicate credential id): %d, want %d", resp.StatusCode, http.StatusConflict)
	}
}
