package accounts

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// ChangePassword replaces the user's own password. current must verify
// against the stored hash, and a temporary password counts only until it
// expires (ErrTemporaryPasswordExpired, checked only once it verified);
// next must pass auth.CheckPasswordPolicy and differ from current.
// In one transaction it stores next, ends a forced change (the flag and the
// temporary password's expiry) and revokes every session of the user except
// keepSessionID, the one making the change. Neither password ever appears in
// an error.
//
// The slow work — verifying current and hashing next — happens before the
// account's row is locked, so the lock is held only for the writes. Under
// the lock the stored hash is read again and must still be the one current
// was verified against: a concurrent change, or an administrator's
// temporary password, that slipped in between makes current no longer the
// password (ErrInvalidCredentials).
func (s *Service) ChangePassword(ctx context.Context, userID, keepSessionID uuid.UUID, current, next string) error {
	st, err := s.store.ReadPassword(ctx, userID)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if err := s.checkPasswordChange(st, current, next); err != nil {
		return err
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.store.InTx(ctx, func(ctx context.Context) error {
		locked, err := s.store.LockPassword(ctx, userID)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if locked.Hash == nil || *locked.Hash != *st.Hash {
			return ErrInvalidCredentials
		}
		if s.temporaryExpired(locked) {
			return ErrTemporaryPasswordExpired
		}
		if err := s.store.SetPassword(ctx, userID, hash, true); err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if err := s.store.RevokeSessionsExcept(ctx, userID, keepSessionID); err != nil {
			return fmt.Errorf("revoke other sessions: %w", err)
		}
		return nil
	})
}

// checkPasswordChange is ChangePassword's checks against the state it read:
// current verifies (and has not expired, if it is a temporary password),
// and next passes the policy and differs from current. An account without
// a password is refused, so st.Hash is not nil when it returns nil.
func (s *Service) checkPasswordChange(st PasswordState, current, next string) error {
	if st.Hash == nil {
		return ErrInvalidCredentials
	}
	ok, err := auth.VerifyPassword(*st.Hash, current)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return ErrInvalidCredentials
	}
	if s.temporaryExpired(st) {
		return ErrTemporaryPasswordExpired
	}
	if err := auth.CheckPasswordPolicy(next); err != nil {
		return err
	}
	if next == current {
		return ErrSamePassword
	}
	return nil
}

// temporaryExpired reports whether st is a temporary password past its
// expiry.
func (s *Service) temporaryExpired(st PasswordState) bool {
	return st.TempPasswordExpiresAt != nil && !s.now().Before(*st.TempPasswordExpiresAt)
}
