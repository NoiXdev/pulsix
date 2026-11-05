package accounts

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// TemporaryPasswordTTL is how long an administrator's temporary password
// signs in.
const TemporaryPasswordTTL = 24 * time.Hour

// temporaryPasswordAlphabet leaves out the characters that read alike
// (i, l, o, 0, 1), since a person types the password an administrator
// passed on. 20 characters from its 31 carry about 99 bits.
const temporaryPasswordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

const (
	temporaryPasswordGroups    = 4
	temporaryPasswordGroupSize = 5
)

// TemporaryPassword is a generated password, shown to the administrator
// once and stored only as a hash.
type TemporaryPassword struct {
	Password  string
	ExpiresAt time.Time
}

// SetTemporaryPassword gives target a generated password that expires
// after TemporaryPasswordTTL and must be changed at the next sign-in, ends
// every session of target and voids every unused reset link of target, in
// one transaction. An administrator does this to lock out someone who may
// have the account, maybe its mailbox too: a link issued before must not
// let them choose a password instead (and the forgotten-password route
// issues none while the change is pending, requestReset). The person is told by
// mail (Notice) unless the account is deactivated; the password itself is
// only in the result. The common checks (checkActor, withAccount) apply.
func (s *Service) SetTemporaryPassword(ctx context.Context, actorIsAdmin bool, actor, target uuid.UUID) (TemporaryPassword, Notice, error) {
	if err := checkActor(actorIsAdmin, actor, target); err != nil {
		return TemporaryPassword{}, "", err
	}
	password, err := generateTemporaryPassword(s.rnd)
	if err != nil {
		return TemporaryPassword{}, "", fmt.Errorf("generate temporary password: %w", err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return TemporaryPassword{}, "", fmt.Errorf("hash temporary password: %w", err)
	}
	expiresAt := s.now().Add(TemporaryPasswordTTL)
	var active bool
	err = s.withAccount(ctx, target, func(ctx context.Context, acct LockedAccount) error {
		active = acct.active()
		if err := s.store.SetTemporaryPassword(ctx, target, hash, expiresAt); err != nil {
			return fmt.Errorf("set temporary password: %w", err)
		}
		if err := s.store.RevokeAllSessions(ctx, target); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		if err := s.store.VoidResetTokens(ctx, target, s.now()); err != nil {
			return fmt.Errorf("void reset tokens: %w", err)
		}
		return nil
	})
	if err != nil {
		return TemporaryPassword{}, "", err
	}
	return TemporaryPassword{Password: password, ExpiresAt: expiresAt}, s.notify(ctx, target, active, noticeTemporaryPassword), nil
}

// ResetSecondFactor removes target's TOTP secret, recovery codes and every
// passkey, ends every session of target and voids every unused reset link
// of target, in one transaction: until a new factor is enrolled, whoever
// sets the password can enrol one, so a link issued before the reset must
// not be the way in. At the next sign-in the person enrols a new factor.
// The person is told by mail
// (Notice) unless the account is deactivated. The common checks
// (checkActor, withAccount) apply.
func (s *Service) ResetSecondFactor(ctx context.Context, actorIsAdmin bool, actor, target uuid.UUID) (Notice, error) {
	if err := checkActor(actorIsAdmin, actor, target); err != nil {
		return "", err
	}
	var active bool
	err := s.withAccount(ctx, target, func(ctx context.Context, acct LockedAccount) error {
		active = acct.active()
		if err := s.store.DeleteSecondFactors(ctx, target); err != nil {
			return fmt.Errorf("delete second factors: %w", err)
		}
		if err := s.store.RevokeAllSessions(ctx, target); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		if err := s.store.VoidResetTokens(ctx, target, s.now()); err != nil {
			return fmt.Errorf("void reset tokens: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return s.notify(ctx, target, active, noticeSecondFactorReset), nil
}

// EndSessions signs target out everywhere. API tokens are not touched:
// they are managed per organization. The common checks (checkActor,
// withAccount) apply.
func (s *Service) EndSessions(ctx context.Context, actorIsAdmin bool, actor, target uuid.UUID) error {
	if err := checkActor(actorIsAdmin, actor, target); err != nil {
		return err
	}
	return s.withAccount(ctx, target, func(ctx context.Context, _ LockedAccount) error {
		if err := s.store.RevokeAllSessions(ctx, target); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		return nil
	})
}

// Reactivate lifts target's deactivation. Sessions and tokens revoked when
// it was deactivated stay revoked, and in the same transaction every
// session of target is revoked again: deactivation disables the account
// and revokes its sessions in two steps, not one transaction, so a session
// could have outlived it, and must not come back to life now. For the same
// reason every unused reset link of target is voided: one issued before
// the deactivation must not work again after it. checkActor applies, and
// an unknown account is ErrNotFound (withLockedAccount), but an account
// whose invitation is pending is not refused (backlog 7.82): reactivation
// sets no password, so a deactivated invitee is reactivated with the
// invitation still pending, and accepting it still sets their own.
func (s *Service) Reactivate(ctx context.Context, actorIsAdmin bool, actor, target uuid.UUID) error {
	if err := checkActor(actorIsAdmin, actor, target); err != nil {
		return err
	}
	return s.withLockedAccount(ctx, target, func(ctx context.Context, _ LockedAccount) error {
		if err := s.store.Reactivate(ctx, target); err != nil {
			return fmt.Errorf("reactivate: %w", err)
		}
		if err := s.store.RevokeAllSessions(ctx, target); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		if err := s.store.VoidResetTokens(ctx, target, s.now()); err != nil {
			return fmt.Errorf("void reset tokens: %w", err)
		}
		return nil
	})
}

// checkActor is the part of the common checks every account action makes
// before reading the account: only an instance admin acts
// (ErrForbidden), and never on their own account (ErrNotOnOwnAccount),
// for which Account security is the place.
func checkActor(actorIsAdmin bool, actor, target uuid.UUID) error {
	if !actorIsAdmin {
		return ErrForbidden
	}
	if actor == target {
		return ErrNotOnOwnAccount
	}
	return nil
}

// withAccount runs fn in one transaction with target's row locked, after
// the rest of the common checks: an unknown account is ErrNotFound, and an
// account whose invitation is still pending (no password) is
// ErrInvitePending — no action may hand a password, or a way to one, to an
// account its owner never claimed. Deactivation (in tenancy) and
// reactivation (withLockedAccount) are the actions allowed there. fn gets
// what the lock read, so an action can tell a deactivated account from an
// active one.
func (s *Service) withAccount(ctx context.Context, target uuid.UUID, fn func(ctx context.Context, acct LockedAccount) error) error {
	return s.withLockedAccount(ctx, target, func(ctx context.Context, acct LockedAccount) error {
		if !acct.HasPassword {
			return ErrInvitePending
		}
		return fn(ctx, acct)
	})
}

// withLockedAccount runs fn in one transaction with target's row locked; an
// unknown account is ErrNotFound. It makes no other check: withAccount adds
// the pending-invitation one, which only Reactivate goes without.
func (s *Service) withLockedAccount(ctx context.Context, target uuid.UUID, fn func(ctx context.Context, acct LockedAccount) error) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		acct, err := s.store.LockAccount(ctx, target)
		if err != nil {
			return fmt.Errorf("lock account: %w", err)
		}
		return fn(ctx, acct)
	})
}

// generateTemporaryPassword draws 20 characters uniformly from
// temporaryPasswordAlphabet and groups them xxxxx-xxxxx-xxxxx-xxxxx. A
// byte is used only below the largest multiple of the alphabet's size, so
// no character is likelier than another.
func generateTemporaryPassword(rnd io.Reader) (string, error) {
	const n = temporaryPasswordGroups * temporaryPasswordGroupSize
	size := len(temporaryPasswordAlphabet)
	limit := 256 - 256%size
	chars := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(chars) < n {
		if _, err := io.ReadFull(rnd, buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < limit && len(chars) < n {
				chars = append(chars, temporaryPasswordAlphabet[int(b)%size])
			}
		}
	}
	groups := make([]string, temporaryPasswordGroups)
	for i := range groups {
		groups[i] = string(chars[i*temporaryPasswordGroupSize : (i+1)*temporaryPasswordGroupSize])
	}
	return strings.Join(groups, "-"), nil
}
