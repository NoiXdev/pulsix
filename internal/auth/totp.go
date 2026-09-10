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
	"github.com/noixdev/pulsix/internal/db/gen"
)

// totpStep is the TOTP time-step size (RFC 6238 default, and what
// pquerna/otp's package-level Generate/GenerateCode use).
const totpStep = 30 * time.Second

// totpSkewSteps is how many steps on either side of the current one are
// still accepted, tolerating small clock drift between client and server.
const totpSkewSteps = 1

// ErrInvalidTOTPCode is returned by ConfirmEnrol when the supplied code does
// not validate. It is a distinct sentinel (rather than a generic error) so
// the transport layer can tell "the user typed the wrong code" (400 -
// mapped explicitly) apart from a failed recovery-code write or other
// internal error after the code itself checked out (500, logged): both are
// non-nil errors from the same call, but only one of them is safe to
// describe to the client as "invalid code".
var ErrInvalidTOTPCode = errors.New("invalid totp code")

// TOTP manages time-based one-time passwords and recovery codes.
type TOTP struct {
	q      *gen.Queries
	box    *crypto.Box
	issuer string
	now    func() time.Time
}

// NewTOTP builds the service. issuer is shown in authenticator apps. now is
// the clock used to compute and validate the 30 s step, injected the same
// way as auth.NewSessions so replay-window behaviour can be driven
// deterministically from tests instead of only through wall-clock timing.
func NewTOTP(q *gen.Queries, box *crypto.Box, issuer string, now func() time.Time) *TOTP {
	return &TOTP{q: q, box: box, issuer: issuer, now: now}
}

// BeginEnrol creates an unconfirmed secret and returns it for the QR code.
func (t *TOTP) BeginEnrol(ctx context.Context, userID uuid.UUID, email string) (string, string, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: t.issuer, AccountName: email, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return "", "", err
	}
	sealed, err := t.box.Seal([]byte(key.Secret()))
	if err != nil {
		return "", "", err
	}
	if err := t.q.UpsertTOTPSecret(ctx, gen.UpsertTOTPSecretParams{UserID: userID, SecretSealed: sealed}); err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// ConfirmEnrol validates the first code, activates TOTP and issues 8 recovery codes.
func (t *TOTP) ConfirmEnrol(ctx context.Context, userID uuid.UUID, code string) ([]string, error) {
	ok, err := t.verifyAndConsume(ctx, userID, code, false)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidTOTPCode
	}
	if err := t.q.ConfirmTOTP(ctx, userID); err != nil {
		return nil, err
	}
	if err := t.q.ReplaceRecoveryCodes(ctx, userID); err != nil {
		return nil, err
	}
	codes := make([]string, 8)
	for i := range codes {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		c := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
		codes[i] = c[:5] + "-" + c[5:10] + "-" + c[10:15]
		sum := sha256.Sum256([]byte(codes[i]))
		if err := t.q.InsertRecoveryCode(ctx, gen.InsertRecoveryCodeParams{UserID: userID, CodeHash: sum[:]}); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// Verify checks a code against a confirmed secret and rejects replays within a step.
func (t *TOTP) Verify(ctx context.Context, userID uuid.UUID, code string) (bool, error) {
	return t.verifyAndConsume(ctx, userID, code, true)
}

// verifyAndConsume validates code against the user's secret and, only on a
// valid code, atomically claims the step it actually matched via
// SetTOTPLastStep's conditional UPDATE. requireConfirmed gates whether an
// unconfirmed (still-enrolling) secret may be used: ConfirmEnrol needs to
// validate the very first code before confirmed_at is set, but Verify (the
// login/step-up/disable path) must never accept an unconfirmed secret.
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
	row, err := t.q.GetTOTPSecret(ctx, userID)
	if err != nil {
		return false, nil
	}
	if requireConfirmed && !row.ConfirmedAt.Valid {
		return false, nil
	}
	secret, err := t.box.Open(row.SecretSealed)
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
	n, err := t.q.SetTOTPLastStep(ctx, gen.SetTOTPLastStepParams{UserID: userID, LastUsedStep: step})
	if err != nil {
		return false, err
	}
	return n == 1, nil // 0 rows means this step was already consumed (or an older one)
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

// VerifyRecovery consumes one recovery code.
func (t *TOTP) VerifyRecovery(ctx context.Context, userID uuid.UUID, code string) (bool, error) {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(code))))
	n, err := t.q.UseRecoveryCode(ctx, gen.UseRecoveryCodeParams{UserID: userID, CodeHash: sum[:]})
	return n == 1, err
}

// Enabled reports whether the user has a confirmed TOTP secret.
func (t *TOTP) Enabled(ctx context.Context, userID uuid.UUID) (bool, error) {
	row, err := t.q.GetTOTPSecret(ctx, userID)
	if err != nil {
		return false, nil
	}
	return row.ConfirmedAt.Valid, nil
}

// Disable removes the secret and recovery codes.
func (t *TOTP) Disable(ctx context.Context, userID uuid.UUID) error {
	if err := t.q.ReplaceRecoveryCodes(ctx, userID); err != nil {
		return err
	}
	return t.q.DeleteTOTP(ctx, userID)
}
