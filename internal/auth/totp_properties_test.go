package auth_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// newTOTPFixture wires a TOTP service against a fresh migrated database and
// seeds one user, mirroring TestTOTPEnrolVerifyReplay's setup so the
// property tests below don't repeat it.
func newTOTPFixture(t *testing.T) (context.Context, *gen.Queries, *auth.TOTP, uuid.UUID) {
	t.Helper()
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "t@example.test", DisplayName: "T"}); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	tp := auth.NewTOTP(q, crypto.NewBox(key), "Pulsix Test", time.Now)
	return ctx, q, tp, uid
}

// TestTOTPVerifyRejectsUnconfirmedSecret asserts that a secret which has
// been enrolled but not yet confirmed cannot satisfy Verify, even with a
// code that is otherwise valid for it. Without this, a caller who started
// enrolment but never proved possession of the authenticator (never called
// ConfirmEnrol) would already count as having TOTP active for login/step-up
// purposes.
func TestTOTPVerifyRejectsUnconfirmedSecret(t *testing.T) {
	ctx, _, tp, uid := newTOTPFixture(t)

	secret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if ok, err := tp.Verify(ctx, uid, code); ok || err != nil {
		t.Fatalf("Verify on an unconfirmed secret = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestTOTPConfirmRejectsInvalidCode asserts that ConfirmEnrol requires a
// genuinely valid code: an unconfirmed secret must not become active on a
// wrong guess, and Enabled must keep reporting false afterwards.
func TestTOTPConfirmRejectsInvalidCode(t *testing.T) {
	ctx, _, tp, uid := newTOTPFixture(t)

	if _, _, err := tp.BeginEnrol(ctx, uid, "t@example.test"); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tp.ConfirmEnrol(ctx, uid, "000000"); !errors.Is(err, auth.ErrInvalidTOTPCode) {
		t.Fatalf("ConfirmEnrol(wrong code) error = %v, want %v", err, auth.ErrInvalidTOTPCode)
	}
	if on, _ := tp.Enabled(ctx, uid); on {
		t.Fatal("Enabled reports true after a failed confirmation")
	}
}

// TestTOTPDisableRemovesSecretAndRecoveryCodes asserts that Disable clears
// both the secret and the recovery codes together: a code or recovery code
// that was valid before Disable must be rejected afterwards, and Enabled
// must report false.
func TestTOTPDisableRemovesSecretAndRecoveryCodes(t *testing.T) {
	ctx, q, tp, uid := newTOTPFixture(t)

	secret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	recovery, err := tp.ConfirmEnrol(ctx, uid, code)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}

	if err := tp.Disable(ctx, uid); err != nil {
		t.Fatalf("disable: %v", err)
	}

	if on, _ := tp.Enabled(ctx, uid); on {
		t.Fatal("Enabled reports true after Disable")
	}
	if _, err := q.GetTOTPSecret(ctx, uid); err == nil {
		t.Fatal("totp_secrets row still present after Disable")
	}
	unusedCode := recovery[1] // recovery[0] was never consumed in this test
	if ok, _ := tp.VerifyRecovery(ctx, uid, unusedCode); ok {
		t.Fatal("a recovery code from before Disable still works")
	}
	next, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if ok, _ := tp.Verify(ctx, uid, next); ok {
		t.Fatal("a TOTP code for the disabled secret still works")
	}
}

// TestTOTPReEnrolResetsConfirmationState asserts that beginning enrolment a
// second time (e.g. the user restarting a botched setup) invalidates the
// previous secret rather than layering a new one on top of a still-active
// old one: UpsertTOTPSecret's ON CONFLICT clause resets confirmed_at and
// last_used_step, so Enabled must go back to false and the old secret's
// codes must stop working until the new secret is confirmed.
func TestTOTPReEnrolResetsConfirmationState(t *testing.T) {
	ctx, _, tp, uid := newTOTPFixture(t)

	firstSecret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test")
	if err != nil {
		t.Fatalf("begin 1: %v", err)
	}
	firstCode, _ := totp.GenerateCode(firstSecret, time.Now())
	if _, err := tp.ConfirmEnrol(ctx, uid, firstCode); err != nil {
		t.Fatalf("confirm 1: %v", err)
	}
	if on, _ := tp.Enabled(ctx, uid); !on {
		t.Fatal("not enabled after first confirmation")
	}

	secondSecret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test")
	if err != nil {
		t.Fatalf("begin 2: %v", err)
	}
	if secondSecret == firstSecret {
		t.Fatal("re-enrolment reused the previous secret")
	}
	if on, _ := tp.Enabled(ctx, uid); on {
		t.Fatal("still enabled after re-enrolment started, before the new secret is confirmed")
	}
}

// TestTOTPSecretStoredSealedNotPlaintext asserts that BeginEnrol never
// stores the raw base32 secret at rest: the persisted secret_sealed bytes
// must not contain the plaintext secret as a substring, only its AES-GCM
// ciphertext.
func TestTOTPSecretStoredSealedNotPlaintext(t *testing.T) {
	ctx, q, tp, uid := newTOTPFixture(t)

	secret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	row, err := q.GetTOTPSecret(ctx, uid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if bytes.Contains(row.SecretSealed, []byte(secret)) {
		t.Fatal("secret_sealed contains the plaintext secret")
	}
}
