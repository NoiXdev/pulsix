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
// The account's row stays locked from the read to the write, so a
// concurrent change, or an administrator's temporary password, cannot slip
// in between the verification and the new hash.
func (s *Service) ChangePassword(ctx context.Context, userID, keepSessionID uuid.UUID, current, next string) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		st, err := s.store.LockPassword(ctx, userID)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
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
		if st.TempPasswordExpiresAt != nil && !s.now().Before(*st.TempPasswordExpiresAt) {
			return ErrTemporaryPasswordExpired
		}
		if err := auth.CheckPasswordPolicy(next); err != nil {
			return err
		}
		if next == current {
			return ErrSamePassword
		}
		hash, err := auth.HashPassword(next)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
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
