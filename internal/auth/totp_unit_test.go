package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/crypto"
)

// fakeTOTPStore is a hand-rolled auth.TOTPStore, the seam Finding 4 asked
// for: every branch of TOTP below can now be driven from a fast, in-memory
// fake instead of a real database whose queries are string-matched by
// generated-SQL comments. Only the fields a given test cares about need to
// be set; the zero value behaves like "no secret enrolled yet, no writes
// fail".
type fakeTOTPStore struct {
	// GetTOTPSecret
	hasSecret bool
	secret    auth.TOTPSecretRow
	secretErr error // when non-nil, returned as-is instead of ErrNoTOTPSecret/secret

	// UpsertTOTPSecret
	upsertErr   error
	upsertCalls int

	// SetTOTPLastStep
	setLastStepOK    bool
	setLastStepErr   error
	setLastStepSteps []int64

	// ConfirmEnrol
	confirmEnrolErr   error
	confirmEnrolCalls [][][]byte

	// ReplaceRecoveryCodes / DeleteTOTP (Disable's two writes)
	replaceErr   error
	replaceCalls int
	deleteErr    error
	deleteCalls  int

	// UseRecoveryCode
	useRecoveryOK  bool
	useRecoveryErr error

	// CountUnusedRecoveryCodes
	unusedRecovery int
}

func (f *fakeTOTPStore) GetTOTPSecret(context.Context, uuid.UUID) (auth.TOTPSecretRow, error) {
	if f.secretErr != nil {
		return auth.TOTPSecretRow{}, f.secretErr
	}
	if !f.hasSecret {
		return auth.TOTPSecretRow{}, auth.ErrNoTOTPSecret
	}
	return f.secret, nil
}

func (f *fakeTOTPStore) UpsertTOTPSecret(_ context.Context, _ uuid.UUID, sealed []byte) error {
	f.upsertCalls++
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.hasSecret = true
	f.secret = auth.TOTPSecretRow{SecretSealed: sealed, Confirmed: false}
	return nil
}

func (f *fakeTOTPStore) SetTOTPLastStep(_ context.Context, _ uuid.UUID, step int64) (bool, error) {
	f.setLastStepSteps = append(f.setLastStepSteps, step)
	if f.setLastStepErr != nil {
		return false, f.setLastStepErr
	}
	return f.setLastStepOK, nil
}

func (f *fakeTOTPStore) ConfirmEnrol(_ context.Context, _ uuid.UUID, hashes [][]byte) error {
	f.confirmEnrolCalls = append(f.confirmEnrolCalls, hashes)
	if f.confirmEnrolErr != nil {
		return f.confirmEnrolErr
	}
	f.secret.Confirmed = true
	return nil
}

func (f *fakeTOTPStore) ReplaceRecoveryCodes(context.Context, uuid.UUID) error {
	f.replaceCalls++
	return f.replaceErr
}

func (f *fakeTOTPStore) DeleteTOTP(context.Context, uuid.UUID) error {
	f.deleteCalls++
	return f.deleteErr
}

func (f *fakeTOTPStore) CountUnusedRecoveryCodes(context.Context, uuid.UUID) (int, error) {
	return f.unusedRecovery, nil
}

func (f *fakeTOTPStore) UseRecoveryCode(context.Context, uuid.UUID, []byte) (bool, error) {
	if f.useRecoveryErr != nil {
		return false, f.useRecoveryErr
	}
	return f.useRecoveryOK, nil
}

// testTOTPBox and testTOTPNow give the unit tests below a fixed key and a
// fixed clock, so codes can be generated once and compared deterministically
// without any wall-clock flakiness.
func testTOTPBox() *crypto.Box {
	var key [32]byte
	copy(key[:], "unit-test-master-key-0123456789")
	return crypto.NewBox(key)
}

func testTOTPNow() time.Time { return time.Unix(1_700_000_000, 0) }

// sealedConfirmedSecret builds a TOTPSecretRow for a confirmed secret,
// sealed under testTOTPBox, along with a code valid for it at testTOTPNow.
func sealedConfirmedSecret(t *testing.T) (auth.TOTPSecretRow, string) {
	t.Helper()
	secret := "JBSWY3DPEHPK3PXP"
	sealed, err := testTOTPBox().Seal([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(secret, testTOTPNow())
	if err != nil {
		t.Fatal(err)
	}
	return auth.TOTPSecretRow{SecretSealed: sealed, Confirmed: true}, code
}

// TestTOTPEnabledFailsClosedOnStoreError is Finding 1's central assertion:
// a store error other than "no secret enrolled" must propagate, not be
// swallowed into (false, nil). Enabled feeds Service.Login's mfaRequired
// decision, and Login treats mfaRequired == false as "create an
// MFA-complete session" — so this failing open would silently downgrade a
// TOTP-protected account to password-only login on something as ordinary
// as a statement timeout.
func TestTOTPEnabledFailsClosedOnStoreError(t *testing.T) {
	sentinel := errors.New("store unavailable")
	tp := auth.NewTOTP(&fakeTOTPStore{secretErr: sentinel}, testTOTPBox(), "Pulsix Test", testTOTPNow)
	on, err := tp.Enabled(context.Background(), uuid.Must(uuid.NewV7()))
	if on || !errors.Is(err, sentinel) {
		t.Fatalf("Enabled = (%v, %v), want (false, %v)", on, err, sentinel)
	}
}

// TestTOTPEnabledReturnsFalseWhenNeverEnrolled asserts the one error that
// legitimately means "not enabled": ErrNoTOTPSecret.
func TestTOTPEnabledReturnsFalseWhenNeverEnrolled(t *testing.T) {
	tp := auth.NewTOTP(&fakeTOTPStore{}, testTOTPBox(), "Pulsix Test", testTOTPNow)
	on, err := tp.Enabled(context.Background(), uuid.Must(uuid.NewV7()))
	if on || err != nil {
		t.Fatalf("Enabled(never enrolled) = (%v, %v), want (false, nil)", on, err)
	}
}

// TestTOTPVerifyFailsClosedOnStoreError applies Finding 1's same fix to
// verifyAndConsume (Verify's implementation): a database outage must
// surface as an error, not a misleading "wrong code" (false, nil) that
// masks the real problem behind an ordinary-looking 401/403.
func TestTOTPVerifyFailsClosedOnStoreError(t *testing.T) {
	sentinel := errors.New("store unavailable")
	tp := auth.NewTOTP(&fakeTOTPStore{secretErr: sentinel}, testTOTPBox(), "Pulsix Test", testTOTPNow)
	ok, err := tp.Verify(context.Background(), uuid.Must(uuid.NewV7()), "000000")
	if ok || !errors.Is(err, sentinel) {
		t.Fatalf("Verify = (%v, %v), want (false, %v)", ok, err, sentinel)
	}
}

// TestTOTPBeginEnrolFirstTimeIgnoresCode is Finding 2's first case: an
// account with no TOTPStore row succeeds without a code, and the empty
// code it was called with is never used to gate anything.
func TestTOTPBeginEnrolFirstTimeIgnoresCode(t *testing.T) {
	store := &fakeTOTPStore{}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)
	secret, url, err := tp.BeginEnrol(context.Background(), uuid.Must(uuid.NewV7()), "t@example.test", "")
	if err != nil || secret == "" || url == "" {
		t.Fatalf("BeginEnrol(first time, no code) = (%q, %q, %v), want success", secret, url, err)
	}
	if store.upsertCalls != 1 {
		t.Fatalf("UpsertTOTPSecret called %d times, want 1", store.upsertCalls)
	}
}

// TestTOTPBeginEnrolRefusesReEnrolWithoutValidCode is Finding 2's second
// case: once confirmed, a missing or wrong code must refuse the call and
// must not touch the store at all — the existing secret stays exactly as
// it was.
func TestTOTPBeginEnrolRefusesReEnrolWithoutValidCode(t *testing.T) {
	confirmed, _ := sealedConfirmedSecret(t)
	for _, code := range []string{"", "000000"} {
		store := &fakeTOTPStore{hasSecret: true, secret: confirmed}
		tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)
		if _, _, err := tp.BeginEnrol(context.Background(), uuid.Must(uuid.NewV7()), "t@example.test", code); !errors.Is(err, auth.ErrInvalidTOTPCode) {
			t.Fatalf("BeginEnrol(code=%q) err = %v, want %v", code, err, auth.ErrInvalidTOTPCode)
		}
		if store.upsertCalls != 0 {
			t.Fatalf("code=%q: UpsertTOTPSecret called %d times, want 0 — a refused re-enrolment must not touch the store", code, store.upsertCalls)
		}
	}
}

// TestTOTPBeginEnrolAllowsReEnrolWithValidCode is Finding 2's third case: a
// fresh, valid code for the existing confirmed secret lets BeginEnrol
// proceed and issue a new one.
func TestTOTPBeginEnrolAllowsReEnrolWithValidCode(t *testing.T) {
	confirmed, code := sealedConfirmedSecret(t)
	store := &fakeTOTPStore{hasSecret: true, secret: confirmed, setLastStepOK: true}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)
	secret, url, err := tp.BeginEnrol(context.Background(), uuid.Must(uuid.NewV7()), "t@example.test", code)
	if err != nil || secret == "" || url == "" {
		t.Fatalf("BeginEnrol(valid re-enrol code) = (%q, %q, %v), want success", secret, url, err)
	}
	if store.upsertCalls != 1 {
		t.Fatalf("UpsertTOTPSecret called %d times, want 1", store.upsertCalls)
	}
}

// TestTOTPBeginEnrolPropagatesInitialGetError covers the GetTOTPSecret call
// BeginEnrol makes before deciding whether a code is required: a store
// error there must surface, not be treated as "no secret, proceed".
func TestTOTPBeginEnrolPropagatesInitialGetError(t *testing.T) {
	sentinel := errors.New("store unavailable")
	tp := auth.NewTOTP(&fakeTOTPStore{secretErr: sentinel}, testTOTPBox(), "Pulsix Test", testTOTPNow)
	if _, _, err := tp.BeginEnrol(context.Background(), uuid.Must(uuid.NewV7()), "t@example.test", ""); !errors.Is(err, sentinel) {
		t.Fatalf("BeginEnrol err = %v, want %v", err, sentinel)
	}
}

// TestTOTPBeginEnrolPropagatesUpsertError covers Finding 4: BeginEnrol's
// write must surface a store failure instead of reporting success.
func TestTOTPBeginEnrolPropagatesUpsertError(t *testing.T) {
	sentinel := errors.New("upsert failed")
	tp := auth.NewTOTP(&fakeTOTPStore{upsertErr: sentinel}, testTOTPBox(), "Pulsix Test", testTOTPNow)
	if _, _, err := tp.BeginEnrol(context.Background(), uuid.Must(uuid.NewV7()), "t@example.test", ""); !errors.Is(err, sentinel) {
		t.Fatalf("BeginEnrol err = %v, want %v", err, sentinel)
	}
}

// TestTOTPConfirmEnrolPropagatesStoreError covers Finding 3/4 together: the
// atomic store.ConfirmEnrol call failing must surface as ConfirmEnrol's
// error, not be swallowed or reported as an invalid code.
func TestTOTPConfirmEnrolPropagatesStoreError(t *testing.T) {
	unconfirmed, code := sealedConfirmedSecret(t)
	unconfirmed.Confirmed = false
	// The confirmation path reads the secret being enrolled, which lives
	// in the pending column (backlog 1.27); here it is the same material.
	unconfirmed.PendingSealed = unconfirmed.SecretSealed
	sentinel := errors.New("confirm enrol failed")
	store := &fakeTOTPStore{hasSecret: true, secret: unconfirmed, setLastStepOK: true, confirmEnrolErr: sentinel}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)
	if _, err := tp.ConfirmEnrol(context.Background(), uuid.Must(uuid.NewV7()), code); !errors.Is(err, sentinel) {
		t.Fatalf("ConfirmEnrol err = %v, want %v", err, sentinel)
	}
	if len(store.confirmEnrolCalls) != 1 || len(store.confirmEnrolCalls[0]) != 8 {
		t.Fatalf("store.ConfirmEnrol calls = %+v, want one call with 8 hashes", store.confirmEnrolCalls)
	}
}

// TestTOTPDisablePropagatesReplaceRecoveryCodesError and
// TestTOTPDisablePropagatesDeleteTOTPError cover Finding 4 for Disable's
// two sequential writes.
func TestTOTPDisablePropagatesReplaceRecoveryCodesError(t *testing.T) {
	sentinel := errors.New("replace failed")
	store := &fakeTOTPStore{replaceErr: sentinel}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)
	if err := tp.Disable(context.Background(), uuid.Must(uuid.NewV7())); !errors.Is(err, sentinel) {
		t.Fatalf("Disable err = %v, want %v", err, sentinel)
	}
	if store.deleteCalls != 0 {
		t.Fatalf("DeleteTOTP called %d times after ReplaceRecoveryCodes failed, want 0", store.deleteCalls)
	}
}

func TestTOTPDisablePropagatesDeleteTOTPError(t *testing.T) {
	sentinel := errors.New("delete failed")
	store := &fakeTOTPStore{deleteErr: sentinel}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)
	if err := tp.Disable(context.Background(), uuid.Must(uuid.NewV7())); !errors.Is(err, sentinel) {
		t.Fatalf("Disable err = %v, want %v", err, sentinel)
	}
	if store.replaceCalls != 1 {
		t.Fatalf("ReplaceRecoveryCodes called %d times, want 1", store.replaceCalls)
	}
}

// TestTOTPVerifyPropagatesSetTOTPLastStepError covers verifyAndConsume's
// final write, the conditional claim of the matched step.
func TestTOTPVerifyPropagatesSetTOTPLastStepError(t *testing.T) {
	confirmed, code := sealedConfirmedSecret(t)
	sentinel := errors.New("set last step failed")
	store := &fakeTOTPStore{hasSecret: true, secret: confirmed, setLastStepErr: sentinel}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)
	ok, err := tp.Verify(context.Background(), uuid.Must(uuid.NewV7()), code)
	if ok || !errors.Is(err, sentinel) {
		t.Fatalf("Verify = (%v, %v), want (false, %v)", ok, err, sentinel)
	}
}

// TestTOTPVerifyPropagatesOpenError asserts that a secret which fails to
// decrypt (corrupted at rest, or sealed under a different master key) is
// reported as an error rather than silently treated as "no such secret".
// This needs no database at all: Box.Open is pure, deterministic crypto.
func TestTOTPVerifyPropagatesOpenError(t *testing.T) {
	store := &fakeTOTPStore{hasSecret: true, secret: auth.TOTPSecretRow{SecretSealed: []byte("x"), Confirmed: true}}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)
	ok, err := tp.Verify(context.Background(), uuid.Must(uuid.NewV7()), "000000")
	if ok || err == nil {
		t.Fatalf("Verify(undecryptable secret) = (%v, %v), want (false, a decrypt error)", ok, err)
	}
}

// TestTOTPVerifyRecoveryPropagatesStoreError asserts VerifyRecovery does
// not swallow a store failure into "recovery code rejected".
func TestTOTPVerifyRecoveryPropagatesStoreError(t *testing.T) {
	sentinel := errors.New("use recovery code failed")
	tp := auth.NewTOTP(&fakeTOTPStore{useRecoveryErr: sentinel}, testTOTPBox(), "Pulsix Test", testTOTPNow)
	ok, err := tp.VerifyRecovery(context.Background(), uuid.Must(uuid.NewV7()), "somecode")
	if ok || !errors.Is(err, sentinel) {
		t.Fatalf("VerifyRecovery = (%v, %v), want (false, %v)", ok, err, sentinel)
	}
}
