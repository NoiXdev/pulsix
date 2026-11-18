package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// The session store's part in PULSIX_MFA_OPTIONAL: refreshing a
// password-only session's proof, and what finishing an enrolment needs to
// tell an account's first factor from a further one.

// RefreshPasswordProof is MarkMFAComplete for the password step-up of
// PULSIX_MFA_OPTIONAL: it refreshes the complete session id's proof and
// replaces its secret, keeping it password-only, and returns the new cookie
// value. Whether the account still has no second factor is decided in the
// same statement (RefreshPasswordProof), so a factor that is there when it
// runs refuses it with ErrPasswordNotTheFactor and nothing changes. The
// caller has checked that the session is live (LockLive); a session that
// matches nothing here has a factor or is not complete.
func (s *Sessions) RefreshPasswordProof(ctx context.Context, id uuid.UUID) (string, error) {
	secret, sum, err := newSessionSecret()
	if err != nil {
		return "", err
	}
	n, err := s.q.RefreshPasswordProof(ctx, gen.RefreshPasswordProofParams{ID: id, SecretHash: sum})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrPasswordNotTheFactor
	}
	return cookieValue(id, secret), nil
}

// LockLiveForEnrolment is LockLive for finishing an enrolment (confirming
// TOTP, finishing a passkey registration), and reports whether sessionID is
// password-only (PULSIX_MFA_OPTIONAL). A password-only session's enrolment
// takes the account row exclusively (LockAccountForRemoval) instead of in
// key-share mode: it re-checks that the account still has no factor and may
// revoke its tokens, and two such first enrolments, or one and a passkey
// login, must not interleave. Any other session keeps the key-share lock,
// so nothing changes with the mode off. The session is peeked at without a
// lock to choose, and checked again under the lock as LockLive does.
func (s *Sessions) LockLiveForEnrolment(ctx context.Context, userID, sessionID uuid.UUID) (passwordOnly bool, err error) {
	peek, err := s.q.GetSession(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("look up session: %w", err)
	}
	lock := s.LockAccount
	if peek.PasswordOnly {
		lock = s.LockAccountForRemoval
	}
	if err := lock(ctx, userID); err != nil {
		return false, err
	}
	row, err := s.q.GetSession(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("look up session: %w", err)
	}
	if row.UserID != userID {
		return false, ErrNotFound
	}
	return row.PasswordOnly, nil
}

// HasSecondFactor reports whether userID has a confirmed TOTP secret or a
// passkey, in one statement (the test GetSession's password_superseded
// applies). Enrolment asks it under the account's lock.
func (s *Sessions) HasSecondFactor(ctx context.Context, userID uuid.UUID) (bool, error) {
	on, err := s.q.HasSecondFactor(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("check second factor: %w", err)
	}
	return on, nil
}

// RevokeAccountTokens revokes every organization token and personal token
// of userID, with the statements an account's deactivation uses
// (RevokeTokensForUser, RevokePersonalTokensForUser). Enrolment calls it
// when a password-only session enrols the account's first factor: what a
// password alone could mint must not outlive the factor.
func (s *Sessions) RevokeAccountTokens(ctx context.Context, userID uuid.UUID) error {
	if err := s.q.RevokeTokensForUser(ctx, userID); err != nil {
		return fmt.Errorf("revoke organization tokens: %w", err)
	}
	if err := s.q.RevokePersonalTokensForUser(ctx, userID); err != nil {
		return fmt.Errorf("revoke personal tokens: %w", err)
	}
	return nil
}
