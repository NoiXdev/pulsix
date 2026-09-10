package auth

import (
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

// This file is package auth (not auth_test), same pattern as
// totp_step_test.go: waUser's accessor methods and the toWebauthnCredentials
// / fromWebauthnCredential conversions are unexported pure functions with no
// store or network dependency, so they are tested directly here instead of
// only incidentally through BeginRegistration/FinishRegistration's
// (largely empty-credential-list) paths in passkey_test.go.

func TestWaUserAccessors(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	u := waUser{
		u:     User{ID: uid, Email: "a@x", DisplayName: "A"},
		creds: []webauthn.Credential{{ID: []byte("cred")}},
	}
	if got := u.WebAuthnID(); string(got) != string(uid[:]) {
		t.Errorf("WebAuthnID() = %x, want %x", got, uid[:])
	}
	if got := u.WebAuthnName(); got != "a@x" {
		t.Errorf("WebAuthnName() = %q", got)
	}
	if got := u.WebAuthnDisplayName(); got != "A" {
		t.Errorf("WebAuthnDisplayName() = %q", got)
	}
	if got := u.WebAuthnCredentials(); len(got) != 1 || string(got[0].ID) != "cred" {
		t.Errorf("WebAuthnCredentials() = %+v", got)
	}
}

func TestToWebauthnCredentialsMapsAllFields(t *testing.T) {
	cs := []Credential{{
		CredentialID: []byte("id1"), PublicKey: []byte("pk1"), Attestation: "packed",
		Transports: []string{"usb", "nfc"}, SignCount: 7, BackupEligible: true,
	}}
	out := toWebauthnCredentials(cs)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	got := out[0]
	if string(got.ID) != "id1" || string(got.PublicKey) != "pk1" || got.AttestationType != "packed" {
		t.Errorf("got = %+v", got)
	}
	if len(got.Transport) != 2 || got.Transport[0] != protocol.AuthenticatorTransport("usb") || got.Transport[1] != protocol.AuthenticatorTransport("nfc") {
		t.Errorf("Transport = %+v", got.Transport)
	}
	if got.Authenticator.SignCount != 7 {
		t.Errorf("SignCount = %d, want 7", got.Authenticator.SignCount)
	}
	if !got.Flags.BackupEligible {
		t.Error("BackupEligible = false, want true")
	}
}

func TestToWebauthnCredentialsEmpty(t *testing.T) {
	if out := toWebauthnCredentials(nil); len(out) != 0 {
		t.Errorf("toWebauthnCredentials(nil) = %+v, want empty", out)
	}
}

func TestFromWebauthnCredentialMapsAllFields(t *testing.T) {
	cred := &webauthn.Credential{
		ID: []byte("id2"), PublicKey: []byte("pk2"), AttestationType: "none",
		Transport:     []protocol.AuthenticatorTransport{"internal", "hybrid"},
		Authenticator: webauthn.Authenticator{SignCount: 42},
		Flags:         webauthn.CredentialFlags{BackupEligible: true},
	}
	got := fromWebauthnCredential(cred)
	if string(got.CredentialID) != "id2" || string(got.PublicKey) != "pk2" || got.Attestation != "none" {
		t.Errorf("got = %+v", got)
	}
	if len(got.Transports) != 2 || got.Transports[0] != "internal" || got.Transports[1] != "hybrid" {
		t.Errorf("Transports = %+v", got.Transports)
	}
	if got.SignCount != 42 || !got.BackupEligible {
		t.Errorf("SignCount/BackupEligible = %d/%v", got.SignCount, got.BackupEligible)
	}
}
