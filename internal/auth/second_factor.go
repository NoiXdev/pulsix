package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrLastSecondFactor is returned when removing a factor would leave the
// account with no second factor at all. A second factor is mandatory: an
// account without one can do nothing but enrol one (see the API's
// requireEnrolmentSession), so removing the last one is refused rather than
// silently demoting the account to password-only.
var ErrLastSecondFactor = errors.New("an account must keep at least one second factor")

// SecondFactors is the account-level view over both kinds of second factor,
// for the rules that span them: whether the account has any factor at all,
// and that it never loses its last one.
type SecondFactors struct {
	TOTP     *TOTP
	Passkeys *Passkeys
}

// Enrolled reports whether userID has a confirmed TOTP secret or at least
// one passkey. It fails closed: a store error from either check is returned,
// never read as "not enrolled".
func (f SecondFactors) Enrolled(ctx context.Context, userID uuid.UUID) (bool, error) {
	return AnyEnabled(f.TOTP, f.Passkeys).Enabled(ctx, userID)
}

// DisableTOTP removes userID's TOTP secret and recovery codes, unless the
// account has no passkey to fall back on, in which case it returns
// ErrLastSecondFactor and changes nothing. The check and the removal are
// separate statements; Enrolment.DisableTOTP runs them under the account's
// exclusive lock so a concurrent removal cannot slip between them (backlog
// 7.116).
func (f SecondFactors) DisableTOTP(ctx context.Context, userID uuid.UUID) error {
	hasPasskey, err := f.Passkeys.Enabled(ctx, userID)
	if err != nil {
		return err
	}
	if !hasPasskey {
		return ErrLastSecondFactor
	}
	return f.TOTP.Disable(ctx, userID)
}

// DeletePasskey removes the passkey id owned by userID, unless it is the
// account's last second factor — its only passkey and no confirmed TOTP
// secret — in which case it returns ErrLastSecondFactor and changes nothing.
// An id userID does not own is ErrNotFound, exactly as Passkeys.Delete
// reports it. Enrolment.DeletePasskey runs it under the account's exclusive
// lock, which serialises it with a passkey login and with a concurrent
// removal of the other factor (backlog 7.116).
func (f SecondFactors) DeletePasskey(ctx context.Context, userID, id uuid.UUID) error {
	creds, err := f.Passkeys.List(ctx, userID)
	if err != nil {
		return err
	}
	owned := false
	for _, c := range creds {
		if c.ID == id {
			owned = true
			break
		}
	}
	if !owned {
		return ErrNotFound
	}
	if len(creds) == 1 {
		hasTOTP, err := f.TOTP.Enabled(ctx, userID)
		if err != nil {
			return err
		}
		if !hasTOTP {
			return ErrLastSecondFactor
		}
	}
	return f.Passkeys.Delete(ctx, userID, id)
}
