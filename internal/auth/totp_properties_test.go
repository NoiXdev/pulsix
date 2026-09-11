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
// property tests below don't repeat it. It returns both the raw *gen.Queries
// (for tests that need to inspect a row directly) and the real
// auth.SQLTOTPStore-backed service, so these tests keep exercising the
// actual SQL, per Finding 4's requirement that at least one integration
// test per behaviour still covers the real store.
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
	tp := auth.NewTOTP(auth.NewSQLTOTPStore(d), crypto.NewBox(key), "Pulsix Test", time.Now)
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

	secret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test", "")
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

	if _, _, err := tp.BeginEnrol(ctx, uid, "t@example.test", ""); err != nil {
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

	secret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test", "")
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

// TestTOTPBeginEnrolFirstTimeNoCodeRequired asserts the first (Finding 2)
// case against the real store: an account with no TOTP row at all can
// begin enrolment without a code.
func TestTOTPBeginEnrolFirstTimeNoCodeRequired(t *testing.T) {
	ctx, _, tp, uid := newTOTPFixture(t)

	secret, url, err := tp.BeginEnrol(ctx, uid, "t@example.test", "")
	if err != nil || secret == "" || url == "" {
		t.Fatalf("BeginEnrol(no code, first time) = (%q, %q, %v), want success", secret, url, err)
	}
}

// TestTOTPReEnrolWithoutCodeRefused asserts the second (Finding 2) case: once
// TOTP is confirmed, calling BeginEnrol again without a code must be
// refused, and must leave the existing confirmed secret completely intact
// (Enabled still true, the old secret's codes still valid) — not silently
// reset confirmed_at the way UpsertTOTPSecret's ON CONFLICT clause would on
// its own. Without this, one unauthenticated-by-possession call would leave
// the account password-only at the next login, making the fresh-code
// requirement on Disable cosmetic.
func TestTOTPReEnrolWithoutCodeRefused(t *testing.T) {
	ctx, _, tp, uid := newTOTPFixture(t)

	firstSecret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test", "")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	firstCode, _ := totp.GenerateCode(firstSecret, time.Now())
	if _, err := tp.ConfirmEnrol(ctx, uid, firstCode); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	if _, _, err := tp.BeginEnrol(ctx, uid, "t@example.test", ""); !errors.Is(err, auth.ErrInvalidTOTPCode) {
		t.Fatalf("re-enrol without a code: err = %v, want %v", err, auth.ErrInvalidTOTPCode)
	}

	if on, err := tp.Enabled(ctx, uid); !on || err != nil {
		t.Fatalf("Enabled after a refused re-enrolment = (%v, %v), want (true, nil)", on, err)
	}
	next, _ := totp.GenerateCode(firstSecret, time.Now().Add(30*time.Second))
	if ok, err := tp.Verify(ctx, uid, next); !ok || err != nil {
		t.Fatalf("the original secret must still work after a refused re-enrolment, got (%v, %v)", ok, err)
	}
}

// TestTOTPReEnrolWithValidCodeSucceeds asserts the third (Finding 2) case:
// supplying a fresh, valid code for the existing confirmed secret lets
// BeginEnrol issue a new one, exactly like Disable's fresh-code check.
func TestTOTPReEnrolWithValidCodeSucceeds(t *testing.T) {
	ctx, _, tp, uid := newTOTPFixture(t)

	firstSecret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test", "")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	firstCode, _ := totp.GenerateCode(firstSecret, time.Now())
	if _, err := tp.ConfirmEnrol(ctx, uid, firstCode); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	freshCode, _ := totp.GenerateCode(firstSecret, time.Now().Add(30*time.Second))
	secondSecret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test", freshCode)
	if err != nil {
		t.Fatalf("re-enrol with a valid code: %v", err)
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

	secret, _, err := tp.BeginEnrol(ctx, uid, "t@example.test", "")
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
