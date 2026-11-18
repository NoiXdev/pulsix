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

// ErrSessionNotComplete is RequireComplete's answer for a session that is
// live but not complete: pending, or password-only and superseded by a
// factor the account has gained since (GetSession's password_superseded).
var ErrSessionNotComplete = errors.New("session is not complete")

// enrollingSession is what finishing an enrolment needs to know of the
// enrolling session, as lockLiveForEnrolment read it under the account
// lock: whether it is password-only (PULSIX_MFA_OPTIONAL) and whether it is
// complete (mfa_complete; a password-only session is).
type enrollingSession struct {
	passwordOnly bool
	complete     bool
}

// lockLiveForEnrolment is LockLive for finishing an enrolment (confirming
// TOTP, finishing a passkey registration), and reports whether sessionID is
// password-only (PULSIX_MFA_OPTIONAL) and complete. The enrolment of an account's first
// factor takes the account row exclusively (LockAccountForRemoval) instead
// of in key-share mode: it re-checks that the account still has no factor,
// may revoke its tokens, and must not interleave with a second first
// enrolment, a passkey login, or a credential mint (WithCompleteSession,
// which takes the row in key-share mode and so waits for it, then finds
// its session superseded). An account that has a factor keeps the key-share
// lock, as every enrolment did before. The session and the account are
// peeked at without a lock to choose, and the session is checked again
// under the lock as LockLive does; a factor never disappears under a live
// session (removing the last one is refused, and a reset ends the sessions),
// so the peek can only err towards the stronger lock.
func (s *Sessions) lockLiveForEnrolment(ctx context.Context, userID, sessionID uuid.UUID) (enrollingSession, error) {
	if _, err := s.q.GetSession(ctx, sessionID); errors.Is(err, pgx.ErrNoRows) {
		return enrollingSession{}, ErrNotFound
	} else if err != nil {
		return enrollingSession{}, fmt.Errorf("look up session: %w", err)
	}
	enrolled, err := s.HasSecondFactor(ctx, userID)
	if err != nil {
		return enrollingSession{}, err
	}
	lock := s.LockAccount
	if !enrolled {
		lock = s.LockAccountForRemoval
	}
	if err := lock(ctx, userID); err != nil {
		return enrollingSession{}, err
	}
	row, err := s.q.GetSession(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return enrollingSession{}, ErrNotFound
	}
	if err != nil {
		return enrollingSession{}, fmt.Errorf("look up session: %w", err)
	}
	if row.UserID != userID {
		return enrollingSession{}, ErrNotFound
	}
	return enrollingSession{passwordOnly: row.PasswordOnly, complete: row.MfaComplete}, nil
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

// TakePasswordOnlySignin clears userID's password-only sign-in mark
// (users.password_only_signin_at, set whenever a password-only session is
// opened) and reports whether it was set. The first second factor takes it
// under the account's exclusive lock and revokes the account's tokens when
// it was, whichever session enrols and whether the mode is still on.
func (s *Sessions) TakePasswordOnlySignin(ctx context.Context, userID uuid.UUID) (bool, error) {
	marked, err := s.q.TakePasswordOnlySignin(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("take the password-only sign-in mark: %w", err)
	}
	return marked, nil
}

// RequireComplete re-reads sessionID in a statement of its own and refuses
// it unless it is a live, unlocked, complete session of userID: a session
// that ended, or belongs to another account, is ErrUnauthenticated; a
// locked one ErrSessionLocked; a pending one, or a password-only one that a
// factor has superseded, ErrSessionNotComplete. A write that mints a
// credential runs it inside its transaction after taking the account row
// (WithCompleteSession, personal tokens' owner lock), so a first factor
// enrolled meanwhile, which holds that row exclusively until it commits, is
// seen.
func (s *Sessions) RequireComplete(ctx context.Context, userID, sessionID uuid.UUID) error {
	row, err := s.q.GetSession(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthenticated
	}
	if err != nil {
		return fmt.Errorf("look up session: %w", err)
	}
	if row.UserID != userID {
		return ErrUnauthenticated
	}
	if row.LockedAt.Valid {
		return ErrSessionLocked
	}
	if !row.MfaComplete || row.PasswordSuperseded {
		return ErrSessionNotComplete
	}
	return nil
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
