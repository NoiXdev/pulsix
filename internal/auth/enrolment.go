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
// of, the owner's removal of one under the account's exclusive lock
// (backlog 7.116), and the completion of a session whose code was verified
// (backlog 7.118). It owns no state of its own: the rule it encodes is that
// these writes commit together, under the account lock.
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
			return sessionEnded("finish passkey registration", err)
		}
		if err := e.passkeys.store.AddCredential(ctx, p.UserID, name, c); err != nil {
			return err
		}
		if p.MFAComplete {
			return nil
		}
		var err error
		if cookie, err = e.sessions.MarkMFAComplete(ctx, p.SessionID); err != nil {
			return sessionEnded("finish passkey registration", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return cookie, nil
}

// sessionEnded reports a session that matched nothing as ErrUnauthenticated:
// for a request that begins or finishes an enrolment, its own session
// having ended is a sign-in problem, not a missing ceremony or credential
// (ErrNotFound). op names the step for the error's context.
func sessionEnded(op string, err error) error {
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%s: the session has ended: %w", op, ErrUnauthenticated)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// BeginTOTP begins a TOTP enrolment on sessionID's session: it checks code
// against the account's confirmed secret, if it has one, and writes a new
// pending secret, returning it and its otpauth URL.
//
// The pending secret is written only while the session is still live, in
// one instance transaction with the account lock (backlog 7.109): a request
// resolves its session before it reads its body, and an administrator's
// second-factor reset may have ended it since. A session that has ended is
// ErrUnauthenticated and writes nothing. The code check runs before that
// transaction, so a wrong code spends its attempt even then (backlog 7.26).
func (e *Enrolment) BeginTOTP(ctx context.Context, userID, sessionID uuid.UUID, email, code string) (secret, url string, err error) {
	key, sealed, err := e.totp.newEnrolment(ctx, userID, email, code)
	if err != nil {
		return "", "", err
	}
	err = e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := e.sessions.LockLive(ctx, userID, sessionID); err != nil {
			return sessionEnded("begin TOTP enrolment", err)
		}
		return e.totp.store.UpsertTOTPSecret(ctx, userID, sealed)
	})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// DeletePasskey removes the passkey id owned by userID under the account
// row's exclusive lock (Sessions.LockAccountForRemoval), in one instance
// transaction with the last-second-factor check (backlog 7.116). The lock
// serialises the deletion with a passkey login, which takes the same row in
// key-share mode before it re-reads its credential and opens a session, and
// with a concurrent removal of the account's other factor. An unknown
// account, or an id userID does not own, is ErrNotFound; the account's last
// second factor is ErrLastSecondFactor. Either changes nothing.
func (e *Enrolment) DeletePasskey(ctx context.Context, userID, id uuid.UUID) error {
	return e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := e.sessions.LockAccountForRemoval(ctx, userID); err != nil {
			return fmt.Errorf("delete passkey: %w", err)
		}
		return SecondFactors{TOTP: e.totp, Passkeys: e.passkeys}.DeletePasskey(ctx, userID, id)
	})
}

// DisableTOTP removes userID's TOTP secret and recovery codes under the
// account row's exclusive lock, in one instance transaction with the
// last-second-factor check (backlog 7.116): two concurrent removals of the
// account's last two factors run one after the other, and the second sees
// the first's removal and is refused. An unknown account is ErrNotFound;
// TOTP as the account's last second factor is ErrLastSecondFactor. Either
// changes nothing.
func (e *Enrolment) DisableTOTP(ctx context.Context, userID uuid.UUID) error {
	return e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := e.sessions.LockAccountForRemoval(ctx, userID); err != nil {
			return fmt.Errorf("disable TOTP: %w", err)
		}
		return SecondFactors{TOTP: e.totp, Passkeys: e.passkeys}.DisableTOTP(ctx, userID)
	})
}

// CompleteVerifiedSession completes sessionID's session once the caller has
// verified a TOTP or recovery code for userID, and returns its new cookie
// (see Sessions.MarkMFAComplete), which the caller must send.
//
// It takes the account row first (Sessions.LockLive, key share), in the
// order an administrator's second-factor reset takes the account and then
// the sessions (backlog 7.118). Completing a pending session stamps the
// account's last_sign_in_at, so a completion that locked its session row
// first and the reset deadlocked; in this order the completion queues
// behind the reset and then finds its session ended. A session that is no
// longer live is ErrNotFound and changes nothing.
//
// The code is verified before this call, outside its transaction: a
// verification spends a factor attempt (backlog 7.26), and a rollback here
// would refund the attempt a wrong code spent.
func (e *Enrolment) CompleteVerifiedSession(ctx context.Context, userID, sessionID uuid.UUID) (cookie string, err error) {
	err = e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := e.sessions.LockLive(ctx, userID, sessionID); err != nil {
			return fmt.Errorf("complete the verified session: %w", err)
		}
		var err error
		if cookie, err = e.sessions.MarkMFAComplete(ctx, sessionID); err != nil {
			return fmt.Errorf("complete the verified session: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return cookie, nil
}
