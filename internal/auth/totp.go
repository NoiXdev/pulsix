package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/crypto"
)

// totpStep is the TOTP time-step size (RFC 6238 default, and what
// pquerna/otp's package-level Generate/GenerateCode use).
const totpStep = 30 * time.Second

// totpSkewSteps is how many steps on either side of the current one are
// still accepted, tolerating small clock drift between client and server.
const totpSkewSteps = 1

// ErrInvalidTOTPCode is returned when a caller-supplied code does not
// validate: ConfirmEnrol's first code, a re-enrolment's fresh-code check,
// or Verify/VerifyRecovery's caller (via the transport layer) failing the
// same way. It is a distinct sentinel (rather than a generic error) so the
// transport layer can tell "the user typed the wrong code" (400 - mapped
// explicitly) apart from an internal error after the code itself checked
// out (500, logged): both are non-nil errors from the same call, but only
// one of them is safe to describe to the client as "invalid code".
var ErrInvalidTOTPCode = errors.New("invalid totp code")

// ErrNoTOTPSecret is returned by a TOTPStore's GetTOTPSecret when the user
// has no totp_secrets row at all. It is a distinct sentinel from the
// package's general ErrNotFound (rather than reusing it) so that "no user"
// and "no TOTP secret" can never be confused if a future caller starts
// checking one where it meant the other.
var ErrNoTOTPSecret = errors.New("no totp secret")

// ErrTooManyFactorAttempts is returned instead of checking a code while
// the account's second factor is locked after repeated failures (backlog
// 7.26). The code was not looked at, so it says nothing about the code.
var ErrTooManyFactorAttempts = errors.New("too many second-factor attempts")

// The account's own budget for checking its confirmed second factor
// (backlog 7.26). The per-address limit in front of /auth/totp/verify
// does not stop a password holder who spreads guesses over many
// addresses, and six digits with one step of skew either side leave three
// codes in a million valid at any moment. Four attempts are free; the
// fifth and each later one lock the account for a minute, doubling per
// attempt, at most an hour — about two dozen guesses a day once an attack
// is under way, while the owner, who succeeds within a few tries, is never
// locked out for more than an hour. A success gives the budget back.
const (
	factorAttemptsFree = 5
	factorLockBase     = time.Minute
	factorLockMax      = time.Hour
)

// TOTPSecretRow is the store-level view of one totp_secrets row: just
// enough for the service to validate a code and decide whether the factor
// is active. It deliberately does not expose last_used_step — claiming a
// step is the store's job (SetTOTPLastStep), never the service reading a
// value and writing it back non-atomically.
type TOTPSecretRow struct {
	SecretSealed []byte
	Confirmed    bool
	// PendingSealed is the secret of an enrolment in flight, if any. It
	// sits beside the confirmed one so asking for a new factor never
	// takes the working one away (backlog 1.27); ConfirmEnrol is what
	// promotes it, and only its own first code can do that.
	PendingSealed []byte
}

// TOTPStore is what the TOTP service needs from persistence, mirroring how
// Store decouples Service from generated SQL: the service must never
// import gen or pgx directly, and a fake implementation lets tests drive
// every error branch without a database (see totp_store_sql.go for the
// sqlc-backed implementation, and totp_unit_test.go for the fakes).
//
// ConfirmEnrol is one method, not three (mark confirmed, replace recovery
// codes, insert each one), because those writes must be atomic: a failure
// partway through must never leave confirmed_at set with no usable
// recovery codes. The SQL adapter runs it as a single transaction.
type TOTPStore interface {
	UpsertTOTPSecret(ctx context.Context, userID uuid.UUID, secretSealed []byte) error
	// GetTOTPSecret returns ErrNoTOTPSecret if the user has no row, and
	// must not swallow any other error into that same "absent" result:
	// callers rely on being able to tell "not enrolled" apart from "the
	// store failed to tell us" (see Enabled and verifyAndConsume below).
	GetTOTPSecret(ctx context.Context, userID uuid.UUID) (TOTPSecretRow, error)
	// SetTOTPLastStep claims step for userID and reports whether it did:
	// false (with a nil error) means the step was already claimed, by
	// this call or a concurrent one — a replay, not a failure.
	SetTOTPLastStep(ctx context.Context, userID uuid.UUID, step int64) (bool, error)
	// ConfirmEnrol atomically marks the secret confirmed and replaces the
	// user's recovery codes with recoveryCodeHashes.
	ConfirmEnrol(ctx context.Context, userID uuid.UUID, recoveryCodeHashes [][]byte) error
	ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID) error
	DeleteTOTP(ctx context.Context, userID uuid.UUID) error
	// UseRecoveryCode reports whether it consumed an unused recovery code
	// matching codeHash.
	UseRecoveryCode(ctx context.Context, userID uuid.UUID, codeHash []byte) (bool, error)
	// CountUnusedRecoveryCodes reports how many of the account's recovery
	// codes can still be redeemed. Spent codes are kept rather than
	// deleted, so this is deliberately not "how many rows exist".
	CountUnusedRecoveryCodes(ctx context.Context, userID uuid.UUID) (int, error)
	// ReplaceRecoveryCodesWith swaps the account's recovery codes for the
	// given hashes in one transaction, leaving the TOTP secret alone.
	// ConfirmEnrol does the same swap as part of confirming a secret; this
	// is the swap on its own, for an account whose factor is unchanged.
	ReplaceRecoveryCodesWith(ctx context.Context, userID uuid.UUID, recoveryCodeHashes [][]byte) error
	// ClaimFactorAttempt spends one attempt of the account's second-factor
	// budget and reports whether it could: false (with a nil error) means
	// the account is locked, or has no factor at all. It must count and
	// lock in one atomic step.
	ClaimFactorAttempt(ctx context.Context, userID uuid.UUID, now time.Time) (bool, error)
	// ClearFactorAttempts gives the account its whole budget back.
	ClearFactorAttempts(ctx context.Context, userID uuid.UUID) error
}

// TOTP manages time-based one-time passwords and recovery codes.
type TOTP struct {
	store  TOTPStore
	box    *crypto.Box
	issuer string
	now    func() time.Time
}

// NewTOTP builds the service. issuer is shown in authenticator apps. now is
// the clock used to compute and validate the 30 s step, injected the same
// way as auth.NewSessions so replay-window behaviour can be driven
// deterministically from tests instead of only through wall-clock timing.
func NewTOTP(store TOTPStore, box *crypto.Box, issuer string, now func() time.Time) *TOTP {
	return &TOTP{store: store, box: box, issuer: issuer, now: now}
}

// newEnrolment is an enrolment's begin up to the write: it checks code
// against a confirmed secret, if the account has one, and draws the new
// secret, returning it with its sealed form for UpsertTOTPSecret. A wrong
// code spends a second-factor attempt (Verify) in a write of its own, so a
// caller that runs the upsert in a transaction cannot roll that spend back
// (backlog 7.26).
//
// If the account already has a *confirmed* secret, code must be a fresh,
// valid code for that existing secret: without this, one unauthenticated
// call would silently reset confirmed_at (UpsertTOTPSecret's ON CONFLICT
// clause) and leave the account password-only at the next login, making
// the fresh-code requirement on Disable cosmetic — an attacker holding an
// MFA-complete session could strip the factor either way. A first-time
// enrolment (no row yet) or resuming an abandoned, never-confirmed
// enrolment needs no code: there is no active factor yet to prove
// possession of.
//
// The only begin is Enrolment.BeginTOTP, which writes the pending secret
// only on a session that is still live (backlog 7.109); the tests' seam
// without that check lives in export_test.go.
func (t *TOTP) newEnrolment(ctx context.Context, userID uuid.UUID, email, code string) (*otp.Key, []byte, error) {
	row, err := t.store.GetTOTPSecret(ctx, userID)
	switch {
	case errors.Is(err, ErrNoTOTPSecret):
		// No existing factor: nothing to prove possession of.
	case err != nil:
		return nil, nil, err
	case row.Confirmed:
		ok, verr := t.Verify(ctx, userID, code)
		if verr != nil {
			return nil, nil, verr
		}
		if !ok {
			return nil, nil, ErrInvalidTOTPCode
		}
	}

	key, err := totp.Generate(totp.GenerateOpts{Issuer: t.issuer, AccountName: email, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return nil, nil, err
	}
	sealed, err := t.box.Seal([]byte(key.Secret()))
	if err != nil {
		return nil, nil, err
	}
	return key, sealed, nil
}

// ConfirmEnrol validates the first code, activates TOTP and issues 8
// recovery codes. Activation and the recovery codes are written by a
// single TOTPStore.ConfirmEnrol call so a failure partway through never
// leaves a confirmed factor with no usable recovery codes.
func (t *TOTP) ConfirmEnrol(ctx context.Context, userID uuid.UUID, code string) ([]string, error) {
	ok, err := t.verifyAndConsume(ctx, userID, code, false)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidTOTPCode
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := t.store.ConfirmEnrol(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// recoveryCodeCount is how many codes a set holds. Eight is enough that
// losing the authenticator stays survivable for a long while, and few
// enough that a person will actually write them down.
const recoveryCodeCount = 8

// newRecoveryCodes mints one full set and returns the codes to show once
// alongside the hashes to store. Enrolment and regeneration share it, so
// a set is a set whichever door it came out of — the same count, the same
// alphabet, the same grouping — and only one place has to be audited.
func newRecoveryCodes() (codes []string, hashes [][]byte, err error) {
	codes = make([]string, recoveryCodeCount)
	hashes = make([][]byte, recoveryCodeCount)
	for i := range codes {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		c := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
		codes[i] = c[:5] + "-" + c[5:10] + "-" + c[10:15]
		sum := sha256.Sum256([]byte(codes[i]))
		hashes[i] = sum[:]
	}
	return codes, hashes, nil
}

// RegenerateRecoveryCodes issues a fresh set and makes the previous one
// unusable in the same transaction (backlog 1.34). Two situations need
// it and neither has another way out: the set shown once at enrolment
// was never seen, because the response was lost on the way back; or it
// was seen by someone it should not have been. Both are answered by the
// old set ceasing to exist, which is why this replaces rather than tops
// up — a regenerate that left the previous codes redeemable would be
// worse than none at all.
//
// The caller is responsible for having proven the factor first; this is
// deliberately not a second verification point, so that the rule about
// what counts as proof lives in one place.
func (t *TOTP) RegenerateRecoveryCodes(ctx context.Context, userID uuid.UUID) ([]string, error) {
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := t.store.ReplaceRecoveryCodesWith(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// Verify checks a code against a confirmed secret and rejects replays within a step.
func (t *TOTP) Verify(ctx context.Context, userID uuid.UUID, code string) (bool, error) {
	return t.attempt(ctx, userID, func() (bool, error) {
		return t.verifyAndConsume(ctx, userID, code, true)
	})
}

// attempt runs one check of the account's confirmed second factor against
// its budget (backlog 7.26): the attempt is spent before check runs, a
// locked account is refused without running it, and only a success gives
// the budget back. Spending first is what makes it hold against parallel
// guesses. An account with no factor has no budget either, and its
// refusal stays the plain "rejected" it always was.
func (t *TOTP) attempt(ctx context.Context, userID uuid.UUID, check func() (bool, error)) (bool, error) {
	claimed, err := t.store.ClaimFactorAttempt(ctx, userID, t.now())
	if err != nil {
		return false, err
	}
	if !claimed {
		if _, err := t.store.GetTOTPSecret(ctx, userID); errors.Is(err, ErrNoTOTPSecret) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		return false, ErrTooManyFactorAttempts
	}
	ok, err := check()
	if err != nil || !ok {
		return false, err
	}
	if err := t.store.ClearFactorAttempts(ctx, userID); err != nil {
		return false, err
	}
	return true, nil
}

// verifyAndConsume validates code against the user's secret and, only on a
// valid code, atomically claims the step it actually matched via
// SetTOTPLastStep's conditional UPDATE. requireConfirmed gates whether an
// unconfirmed (still-enrolling) secret may be used: ConfirmEnrol needs to
// validate the very first code before confirmed_at is set, but Verify (the
// login/step-up/disable/re-enrolment path) must never accept an
// unconfirmed secret.
//
// GetTOTPSecret's error is handled deliberately, not swallowed: only
// ErrNoTOTPSecret ("this user has never enrolled") means "no code can
// possibly be valid", so that alone becomes (false, nil). Any other error
// (a timeout, a connection reset, ...) is propagated as an error rather
// than folded into the same "code rejected" result — the caller (Login, in
// particular) must fail closed on a database outage rather than silently
// treat it as "MFA not required".
//
// It deliberately does not use totp.Validate: that helper tolerates a
// totpSkewSteps window of clock drift by checking neighbouring steps
// internally, but only returns a bool, not which step matched. Recording
// the *current* step regardless of which neighbour actually matched would
// let a code for the next step be "consumed" against the current step's
// counter — the current step then still validates a second, different
// code, and a genuine replay of the next-step code would wrongly succeed
// once that step becomes current. So this checks each candidate step
// itself and records precisely the one the code matched.
func (t *TOTP) verifyAndConsume(ctx context.Context, userID uuid.UUID, code string, requireConfirmed bool) (bool, error) {
	row, err := t.store.GetTOTPSecret(ctx, userID)
	if errors.Is(err, ErrNoTOTPSecret) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if requireConfirmed && !row.Confirmed {
		return false, nil
	}
	// The confirmation path checks the code against the secret being
	// enrolled, the login path against the confirmed one. They are
	// different secrets while a re-enrolment is in flight, and that is
	// the whole point of keeping both (backlog 1.27).
	sealed := row.SecretSealed
	if !requireConfirmed {
		if row.PendingSealed == nil {
			// Nothing is enrolling: there is no first code to accept.
			return false, nil
		}
		sealed = row.PendingSealed
	}
	secret, err := t.box.Open(sealed)
	if err != nil {
		return false, err
	}
	step, ok, err := matchStep(code, string(secret), t.now())
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	n, err := t.store.SetTOTPLastStep(ctx, userID, step)
	if err != nil {
		return false, err
	}
	return n, nil // false means this step was already consumed (or an older one)
}

// matchStep reports the step (Unix time / 30s) that code validates against,
// checking the current step and up to totpSkewSteps neighbours on either
// side to tolerate clock drift, exactly like totp.Validate's default
// Skew: 1 — but returning the matched step instead of only a bool.
func matchStep(code, secret string, now time.Time) (int64, bool, error) {
	current := now.Unix() / int64(totpStep/time.Second)
	for offset := int64(0); offset <= totpSkewSteps; offset++ {
		for _, step := range dedupSteps(current+offset, current-offset) {
			want, err := totp.GenerateCodeCustom(secret, time.Unix(step*int64(totpStep/time.Second), 0), totp.ValidateOpts{
				Period: uint(totpStep / time.Second), Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
			})
			if err != nil {
				return 0, false, err
			}
			if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
				return step, true, nil
			}
		}
	}
	return 0, false, nil
}

// dedupSteps returns [a] when a and b are equal (offset 0) and [a, b]
// otherwise, so matchStep's offset-0 iteration doesn't check the current
// step twice.
func dedupSteps(a, b int64) []int64 {
	if a == b {
		return []int64{a}
	}
	return []int64{a, b}
}

// RecoveryCodesRemaining reports how many recovery codes the account can
// still redeem. The account page shows it so that a set emptying one code
// at a time stops being invisible: the person spending them is the one who
// has already lost their usual factor, and running out silently is how a
// recoverable situation becomes an administrator reset.
func (t *TOTP) RecoveryCodesRemaining(ctx context.Context, userID uuid.UUID) (int, error) {
	return t.store.CountUnusedRecoveryCodes(ctx, userID)
}

// VerifyRecovery consumes one recovery code. It spends the same budget as
// Verify: a recovery code stands in for the factor, so guessing one is
// guessing the factor.
func (t *TOTP) VerifyRecovery(ctx context.Context, userID uuid.UUID, code string) (bool, error) {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(code))))
	return t.attempt(ctx, userID, func() (bool, error) {
		return t.store.UseRecoveryCode(ctx, userID, sum[:])
	})
}

// Enabled reports whether the user has a confirmed TOTP secret.
//
// It fails closed: only ErrNoTOTPSecret ("this user never enrolled") is
// treated as "not enabled". Any other store error (a statement timeout, a
// dropped connection, ...) is propagated rather than swallowed into
// (false, nil) — swallowing it here would let a database hiccup silently
// downgrade a TOTP-protected login to password-only, because Login treats
// mfaRequired == false as "create an MFA-complete session".
func (t *TOTP) Enabled(ctx context.Context, userID uuid.UUID) (bool, error) {
	row, err := t.store.GetTOTPSecret(ctx, userID)
	if errors.Is(err, ErrNoTOTPSecret) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.Confirmed, nil
}

// Disable removes the secret and recovery codes.
func (t *TOTP) Disable(ctx context.Context, userID uuid.UUID) error {
	if err := t.store.ReplaceRecoveryCodes(ctx, userID); err != nil {
		return err
	}
	return t.store.DeleteTOTP(ctx, userID)
}
