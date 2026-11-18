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
// (backlog 7.116), the completion of a session whose code was verified
// (backlog 7.118), and the renewal that replaces a session (backlog 3.94).
// It owns no state of its own: the rule it encodes is that
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
//
// From a password-only session (PULSIX_MFA_OPTIONAL) it is also the
// account's first factor (firstFactor): tokensRevoked reports that every
// API token of the account was revoked with it, and an account that has
// gained a factor since the request resolved its session is
// ErrPasswordNotTheFactor and changes nothing.
func (e *Enrolment) ConfirmTOTP(ctx context.Context, userID, sessionID uuid.UUID, code string) (codes []string, cookie string, tokensRevoked bool, err error) {
	err = e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		// Account row first, in the order the reset takes it, so the two
		// serialise instead of deadlocking on each other's rows.
		passwordOnly, err := e.sessions.LockLiveForEnrolment(ctx, userID, sessionID)
		if err != nil {
			return fmt.Errorf("confirm TOTP: %w", err)
		}
		first, err := e.firstFactor(ctx, userID, passwordOnly)
		if err != nil {
			return fmt.Errorf("confirm TOTP: %w", err)
		}
		codes, err = e.totp.ConfirmEnrol(ctx, userID, code)
		if err != nil {
			return err
		}
		if cookie, err = e.sessions.MarkMFAComplete(ctx, sessionID); err != nil {
			return fmt.Errorf("complete the session after confirming TOTP: %w", err)
		}
		tokensRevoked, err = e.revokeOnFirstFactor(ctx, userID, first, passwordOnly)
		return err
	})
	if err != nil {
		return nil, "", false, err
	}
	return codes, cookie, tokensRevoked, nil
}

// firstFactor runs under the account lock LockLiveForEnrolment took and
// reports whether the account has no second factor yet, so that the one
// being enrolled is its first. For a password-only session an account that
// has one already is ErrPasswordNotTheFactor: the session was complete on
// the password only because the account had none, and the factor that
// appeared since is what it must prove now (the session lookup already
// reads it as pending from its next request on).
func (e *Enrolment) firstFactor(ctx context.Context, userID uuid.UUID, passwordOnly bool) (bool, error) {
	enrolled, err := e.sessions.HasSecondFactor(ctx, userID)
	if err != nil {
		return false, err
	}
	if enrolled && passwordOnly {
		return false, ErrPasswordNotTheFactor
	}
	return !enrolled, nil
}

// revokeOnFirstFactor revokes every token of userID when the account has
// just enrolled its first factor and either the enrolling session is
// password-only or the account ever signed in on its password alone
// (PULSIX_MFA_OPTIONAL; the account's mark, which it clears), and reports
// whether it did. A password alone could mint those tokens, whoever held
// it, and the mode may have been turned off since; the factor would protect
// nothing while they still worked. Any other enrolment revokes nothing.
func (e *Enrolment) revokeOnFirstFactor(ctx context.Context, userID uuid.UUID, first, passwordOnly bool) (bool, error) {
	if !first {
		return false, nil
	}
	marked, err := e.sessions.TakePasswordOnlySignin(ctx, userID)
	if err != nil {
		return false, err
	}
	if !marked && !passwordOnly {
		return false, nil
	}
	if err := e.sessions.RevokeAccountTokens(ctx, userID); err != nil {
		return false, err
	}
	return true, nil
}

// WithCompleteSession runs fn in one instance transaction once it has taken
// userID's account row in key-share mode (Sessions.LockAccount) and
// re-checked, in a statement of its own, that sessionID is still a live,
// complete session of the account (Sessions.RequireComplete). A write that
// mints a credential from a session, such as an organization API token,
// runs inside it: the request checked its session when it began, but the
// account's first factor may commit in between, revoking the account's
// tokens and superseding a password-only session. That enrolment holds the
// row exclusively, so fn either commits before it starts (and its token is
// revoked with the rest) or finds the session refused afterwards.
func (e *Enrolment) WithCompleteSession(ctx context.Context, userID, sessionID uuid.UUID, fn func(ctx context.Context) error) error {
	return e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := e.sessions.LockAccount(ctx, userID); err != nil {
			return err
		}
		if err := e.sessions.RequireComplete(ctx, userID, sessionID); err != nil {
			return err
		}
		return fn(ctx)
	})
}

// FinishPasskey finishes a passkey registration on p's session: it verifies
// the attestation against the ceremony, stores the credential and, when the
// session was pending only for want of a factor, completes it and returns
// its new cookie ("" when the session was already complete and is left as
// it is). p is the Principal the request resolved before it read its body.
//
// A session that is complete on its password alone (p.PasswordOnly,
// PULSIX_MFA_OPTIONAL) is completed the same way: the account has a factor
// from now on, and a password-only session of an account with a factor is
// pending (Sessions.Lookup), so the registering session must carry the new
// factor's proof or it would lock itself out.
//
// Storing the credential and upgrading the session are one transaction, and
// it runs only while the session is still live (backlog 7.109): an
// administrator's second-factor reset ends every session of the account, so
// a registration begun before the reset must not leave a passkey behind it.
// A session that has ended is ErrUnauthenticated and stores nothing. The
// ceremony is consumed either way.
//
// When the passkey is the account's first factor, any recovery codes left
// over are deleted with it: they belong to a confirmed TOTP secret, and the
// account has none. From a password-only session it also revokes the
// account's tokens (tokensRevoked) and refuses, as ConfirmTOTP does, an
// account that has gained a factor since (ErrPasswordNotTheFactor).
func (e *Enrolment) FinishPasskey(ctx context.Context, p Principal, ceremonyID uuid.UUID, name string, response json.RawMessage) (cookie string, tokensRevoked bool, err error) {
	c, err := e.passkeys.verifyRegistration(ctx, p.UserID, ceremonyID, name, response)
	if err != nil {
		return "", false, err
	}
	err = e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		passwordOnly, err := e.sessions.LockLiveForEnrolment(ctx, p.UserID, p.SessionID)
		if err != nil {
			return sessionEnded("finish passkey registration", err)
		}
		first, err := e.firstFactor(ctx, p.UserID, passwordOnly)
		if err != nil {
			return fmt.Errorf("finish passkey registration: %w", err)
		}
		if err := e.passkeys.store.AddCredential(ctx, p.UserID, name, c); err != nil {
			return err
		}
		if first {
			if err := e.totp.store.ReplaceRecoveryCodes(ctx, p.UserID); err != nil {
				return fmt.Errorf("finish passkey registration: remove stray recovery codes: %w", err)
			}
		}
		if tokensRevoked, err = e.revokeOnFirstFactor(ctx, p.UserID, first, passwordOnly); err != nil {
			return err
		}
		if p.MFAComplete && !passwordOnly {
			return nil
		}
		if cookie, err = e.sessions.MarkMFAComplete(ctx, p.SessionID); err != nil {
			return sessionEnded("finish passkey registration", err)
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return cookie, tokensRevoked, nil
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

// RefreshPasswordProof is CompleteVerifiedSession for the password step-up
// of PULSIX_MFA_OPTIONAL (POST /auth/password/reauth), once the caller has
// verified userID's password: it refreshes the complete session sessionID's
// proof in place, keeps it password-only, and returns its new cookie, which
// the caller must send. It takes the account row first (LockLive), like
// CompleteVerifiedSession. The account's lack of a second factor is checked
// in the same statement that writes (Sessions.RefreshPasswordProof): an
// account with a factor at that moment is ErrPasswordNotTheFactor and
// nothing changes. A session that is no longer live is ErrNotFound.
func (e *Enrolment) RefreshPasswordProof(ctx context.Context, userID, sessionID uuid.UUID) (cookie string, err error) {
	err = e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := e.sessions.LockLive(ctx, userID, sessionID); err != nil {
			return fmt.Errorf("refresh the password proof: %w", err)
		}
		var err error
		if cookie, err = e.sessions.RefreshPasswordProof(ctx, sessionID); err != nil {
			return fmt.Errorf("refresh the password proof: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return cookie, nil
}

// RenewSession replaces userID's session oldID with a new MFA-complete one
// (Sessions.Replace) in one instance transaction, once the caller has
// verified the password and a second factor, or a passkey, for userID
// (backlog 3.94). Like CompleteVerifiedSession it runs after the
// verification, outside of it, so a rollback here never refunds the attempt
// a wrong code spent. A session that is no longer live, or is locked, is
// ErrNotFound and nothing changes.
func (e *Enrolment) RenewSession(ctx context.Context, userID, oldID uuid.UUID) (RenewedSession, error) {
	return e.renew(ctx, userID, oldID, false)
}

// RenewSessionByPassword is RenewSession for an account whose password is
// its factor (PULSIX_MFA_OPTIONAL, no second factor): the caller verified
// the password alone, so the new session is password-only
// (Sessions.CreatePasswordOnly) and reads as pending should the account
// have a factor by now or gain one later.
func (e *Enrolment) RenewSessionByPassword(ctx context.Context, userID, oldID uuid.UUID) (RenewedSession, error) {
	return e.renew(ctx, userID, oldID, true)
}

func (e *Enrolment) renew(ctx context.Context, userID, oldID uuid.UUID, passwordOnly bool) (RenewedSession, error) {
	var r RenewedSession
	err := e.tx.WithInstanceTx(ctx, func(ctx context.Context) error {
		var err error
		if r, err = e.sessions.Replace(ctx, userID, oldID, passwordOnly); err != nil {
			return fmt.Errorf("renew the session: %w", err)
		}
		return nil
	})
	if err != nil {
		return RenewedSession{}, err
	}
	return r, nil
}
