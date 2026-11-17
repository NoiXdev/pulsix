package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/google/uuid"
)

// BeginReauth starts a passkey ceremony for a signed-in person to prove
// again that they hold one of their own passkeys (backlog 3.94): the
// step-up of the current session and the passkey renewal both finish it.
//
// Unlike BeginLogin it is not discoverable. It names the account (the
// ceremony is stored bound to userID) and offers that account's own
// credentials and nothing else (allowCredentials), so the browser never
// offers a passkey of another account, and FinishReauth refuses one if it
// is presented anyway. An account without a passkey has nothing to offer:
// ErrNotFound.
func (p *Passkeys) BeginReauth(ctx context.Context, userID uuid.UUID) (json.RawMessage, uuid.UUID, error) {
	u, err := p.loadUser(ctx, userID)
	if err != nil {
		return nil, uuid.Nil, err
	}
	if len(u.creds) == 0 {
		return nil, uuid.Nil, fmt.Errorf("begin re-authentication: the account has no passkey: %w", ErrNotFound)
	}
	opts, sd, err := p.wa.BeginLogin(u)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("begin re-authentication: %w", err)
	}
	cid, err := p.saveCeremony(ctx, ceremonyKindReauth, &userID, sd)
	if err != nil {
		return nil, uuid.Nil, err
	}
	raw, err := json.Marshal(opts)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("marshal options: %w", err)
	}
	return raw, cid, nil
}

// FinishReauth verifies an assertion of one of userID's own passkeys
// against a ceremony BeginReauth began for userID, and advances that
// credential's counter. It opens no session and changes none: the caller
// then refreshes the current session (Enrolment.CompleteVerifiedSession, the
// step-up) or replaces it (Enrolment.RenewSession, the renewal).
//
// The ceremony is consumed whatever the outcome. One that is unknown, used,
// expired, of another kind or begun for another account is ErrNotFound. An
// assertion that does not verify against userID's own credentials — another
// account's passkey among them, whichever user handle it carries — is
// ErrInvalidCredentials, and so is everything a sign-in refuses after the
// signature checks out (refuseVerifiedAssertion). The counter is advanced
// under the account lock, only while the credential is still userID's
// (claimAssertion, shared with sign-in).
func (p *Passkeys) FinishReauth(ctx context.Context, userID, ceremonyID uuid.UUID, response json.RawMessage) error {
	cUser, sd, err := p.takeCeremony(ctx, ceremonyID, ceremonyKindReauth)
	if err != nil {
		return err
	}
	if cUser == nil || *cUser != userID {
		return fmt.Errorf("ceremony %s belongs to another user: %w", ceremonyID, ErrNotFound)
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(response))
	if err != nil {
		// A body that is not an assertion is the caller's failed attempt,
		// not the manager's: it charges the rate limit like a wrong one.
		return fmt.Errorf("parse assertion: %w", ErrInvalidCredentials)
	}
	u, err := p.loadUser(ctx, userID)
	if err != nil {
		return err
	}
	// ValidateLogin checks the ceremony is userID's, that the asserted
	// credential is one of userID's own (allowCredentials and the account's
	// current list), that a user handle, when present, is userID's, and the
	// signature against the stored public key.
	cred, err := p.wa.ValidateLogin(u, *sd, parsed)
	if err != nil {
		return ErrInvalidCredentials
	}
	if err := refuseVerifiedAssertion(ctx, u.u, cred); err != nil {
		return err
	}
	return p.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		return p.claimAssertion(ctx, userID, cred.ID, cred.Authenticator.SignCount)
	})
}
