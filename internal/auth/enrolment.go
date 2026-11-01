// Enrolling a second factor is one state change, not two. The account
// gains a confirmed secret and its recovery codes, and the session that
// was pending only for want of a factor becomes complete — and if the
// second half fails, the first must not stand: the account would then
// carry eight recovery codes nobody ever saw, and the only way back is
// re-enrolling, which issues new ones. This composes the two stores
// inside one transaction so that cannot happen (backlog 1.26).
package auth

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// TxRunner runs fn inside one instance-level write transaction; every
// store built on the same database picks it up from the context.
// *db.DB satisfies it (WithInstanceTx).
type TxRunner interface {
	WithInstanceTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Enrolment composes the writes that finishing a second factor consists
// of. It owns no state of its own: the rule it encodes is that these
// writes commit together.
type Enrolment struct {
	totp     *TOTP
	sessions *Sessions
	tx       TxRunner
}

// NewEnrolment wires the composition.
func NewEnrolment(totp *TOTP, sessions *Sessions, tx TxRunner) *Enrolment {
	return &Enrolment{totp: totp, sessions: sessions, tx: tx}
}

// ConfirmTOTP validates the first code against the enrolling secret,
// confirms it with a fresh set of recovery codes and upgrades the
// session, all in one transaction. The codes are returned exactly once —
// they exist nowhere else in readable form — so a caller that fails to
// deliver them to the user leaves the account in a state only a
// re-enrolment fixes; that is why nothing is committed until the session
// is upgraded too. cookie is the session's new cookie value (see
// Sessions.MarkMFAComplete), which the caller must send.
//
// An invalid code returns ErrInvalidTOTPCode and changes nothing. An
// unknown or already-ended session returns ErrNotFound and changes
// nothing.
func (e *Enrolment) ConfirmTOTP(ctx context.Context, userID, sessionID uuid.UUID, code string) (codes []string, cookie string, err error) {
	err = e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		var err error
		codes, err = e.totp.ConfirmEnrol(ctx, userID, code)
		if err != nil {
			return err
		}
		if cookie, err = e.sessions.MarkMFAComplete(ctx, sessionID); err != nil {
			return fmt.Errorf("complete the session after confirming TOTP: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return codes, cookie, nil
}
