package auth

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// BeginEnrol begins a TOTP enrolment without the session check
// Enrolment.BeginTOTP adds (backlog 7.109): newEnrolment, then the write.
// It exists only in tests, so no handler can take the unchecked path.
func (t *TOTP) BeginEnrol(ctx context.Context, userID uuid.UUID, email, code string) (string, string, error) {
	key, sealed, err := t.newEnrolment(ctx, userID, email, code)
	if err != nil {
		return "", "", err
	}
	if err := t.store.UpsertTOTPSecret(ctx, userID, sealed); err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// FinishRegistration finishes a passkey registration without the session
// check Enrolment.FinishPasskey adds (backlog 7.109): it verifies the
// attestation and stores the credential. It exists only in tests, so no
// handler can take the unchecked path.
func (p *Passkeys) FinishRegistration(ctx context.Context, userID, ceremonyID uuid.UUID, name string, response json.RawMessage) error {
	c, err := p.verifyRegistration(ctx, userID, ceremonyID, name, response)
	if err != nil {
		return err
	}
	return p.store.AddCredential(ctx, userID, name, c)
}
