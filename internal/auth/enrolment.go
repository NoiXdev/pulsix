// Enrolling a second factor is one state change, not two. The account
// gains a confirmed secret and its recovery codes, and the session that
// was pending only for want of a factor becomes complete — and if the
// second half fails, the first must not stand: the account would then
// carry eight recovery codes nobody ever saw, and the only way back is
// re-enrolling, which issues new ones. This composes the two stores
// inside one transaction so that cannot happen (backlog 1.26). The same
// holds for a passkey, and both run only on a session that is still live:
// an administrator's second-factor reset ends every session of the account,
// and an enrolment begun before it must not finish after it (backlog 7.109).
package auth

import (
	"context"
	"encoding/json"
	"errors"
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
	passkeys *Passkeys
	sessions *Sessions
	tx       TxRunner
}

// NewEnrolment wires the composition.
func NewEnrolment(totp *TOTP, passkeys *Passkeys, sessions *Sessions, tx TxRunner) *Enrolment {
	return &Enrolment{totp: totp, passkeys: passkeys, sessions: sessions, tx: tx}
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
// nothing — including one an administrator's second-factor reset ended
// after the request resolved it (backlog 7.109).
func (e *Enrolment) ConfirmTOTP(ctx context.Context, userID, sessionID uuid.UUID, code string) (codes []string, cookie string, err error) {
	err = e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		// Account row first, in the order the reset takes it, so the two
		// serialise instead of deadlocking on each other's rows.
		if err := e.sessions.LockLive(ctx, userID, sessionID); err != nil {
			return fmt.Errorf("confirm TOTP: %w", err)
		}
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

// FinishPasskey finishes a passkey registration on p's session: it verifies
// the attestation against the ceremony, stores the credential and, when the
// session was pending only for want of a factor, completes it and returns
// its new cookie ("" when the session was already complete and is left as
// it is). p is the Principal the request resolved before it read its body.
//
// Storing the credential and upgrading the session are one transaction, and
// it runs only while the session is still live (backlog 7.109): an
// administrator's second-factor reset ends every session of the account, so
// a registration begun before the reset must not leave a passkey behind it.
// A session that has ended is ErrUnauthenticated and stores nothing. The
// ceremony is consumed either way.
func (e *Enrolment) FinishPasskey(ctx context.Context, p Principal, ceremonyID uuid.UUID, name string, response json.RawMessage) (cookie string, err error) {
	c, err := e.passkeys.verifyRegistration(ctx, p.UserID, ceremonyID, name, response)
	if err != nil {
		return "", err
	}
	err = e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := e.sessions.LockLive(ctx, p.UserID, p.SessionID); err != nil {
			return sessionEnded(err)
		}
		if err := e.passkeys.store.AddCredential(ctx, p.UserID, name, c); err != nil {
			return err
		}
		if p.MFAComplete {
			return nil
		}
		var err error
		if cookie, err = e.sessions.MarkMFAComplete(ctx, p.SessionID); err != nil {
			return sessionEnded(err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return cookie, nil
}

// sessionEnded reports a session that matched nothing as ErrUnauthenticated:
// for a request that finishes an enrolment, its own session having ended is
// a sign-in problem, not a missing ceremony or credential (ErrNotFound).
func sessionEnded(err error) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("finish passkey registration: the session has ended: %w", ErrUnauthenticated)
	}
	return fmt.Errorf("finish passkey registration: %w", err)
}
