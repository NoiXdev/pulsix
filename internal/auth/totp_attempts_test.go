package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// Backlog 7.26: the only limit on guessing a second-factor code used to be
// the per-address one in front of /auth/totp/verify, so a password holder
// spreading guesses over many addresses could work through the code space.
// Every check of the account's confirmed factor now spends one attempt of
// the account's own budget first, and a locked account is refused before
// its code is looked at.

func TestTOTPVerifyIsRefusedWhileTheAccountIsLocked(t *testing.T) {
	row, code := sealedConfirmedSecret(t)
	store := &fakeTOTPStore{hasSecret: true, secret: row, setLastStepOK: true, locked: true}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)

	ok, err := tp.Verify(context.Background(), uuid.New(), code)
	if ok || !errors.Is(err, auth.ErrTooManyFactorAttempts) {
		t.Fatalf("Verify on a locked account = %v, %v; want ErrTooManyFactorAttempts", ok, err)
	}
	if len(store.setLastStepSteps) != 0 {
		t.Fatal("a locked account's code was checked anyway")
	}
}

func TestTOTPVerifyRecoveryIsRefusedWhileTheAccountIsLocked(t *testing.T) {
	row, _ := sealedConfirmedSecret(t)
	store := &fakeTOTPStore{hasSecret: true, secret: row, useRecoveryOK: true, locked: true}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)

	ok, err := tp.VerifyRecovery(context.Background(), uuid.New(), "aaaaa-bbbbb-ccccc")
	if ok || !errors.Is(err, auth.ErrTooManyFactorAttempts) {
		t.Fatalf("VerifyRecovery on a locked account = %v, %v; want ErrTooManyFactorAttempts", ok, err)
	}
}

func TestTOTPReEnrolIsRefusedWhileTheAccountIsLocked(t *testing.T) {
	row, code := sealedConfirmedSecret(t)
	store := &fakeTOTPStore{hasSecret: true, secret: row, setLastStepOK: true, locked: true}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)

	if _, _, err := tp.BeginEnrol(context.Background(), uuid.New(), "u@example.test", code); !errors.Is(err, auth.ErrTooManyFactorAttempts) {
		t.Fatalf("BeginEnrol on a locked account = %v; want ErrTooManyFactorAttempts", err)
	}
	if store.upsertCalls != 0 {
		t.Fatal("a locked account got a new pending secret")
	}
}

func TestTOTPEveryCheckSpendsAnAttemptAndOnlyASuccessClearsThem(t *testing.T) {
	row, code := sealedConfirmedSecret(t)
	store := &fakeTOTPStore{hasSecret: true, secret: row, setLastStepOK: true}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)
	ctx := context.Background()

	if ok, err := tp.Verify(ctx, uuid.New(), "000000"); ok || err != nil {
		t.Fatalf("Verify(wrong) = %v, %v", ok, err)
	}
	if ok, err := tp.VerifyRecovery(ctx, uuid.New(), "aaaaa-bbbbb-ccccc"); ok || err != nil {
		t.Fatalf("VerifyRecovery(wrong) = %v, %v", ok, err)
	}
	if store.claimCalls != 2 || store.clearCalls != 0 {
		t.Fatalf("after two failures: %d claims, %d clears; want 2, 0", store.claimCalls, store.clearCalls)
	}
	if ok, err := tp.Verify(ctx, uuid.New(), code); !ok || err != nil {
		t.Fatalf("Verify(valid) = %v, %v", ok, err)
	}
	if store.claimCalls != 3 || store.clearCalls != 1 {
		t.Fatalf("after a success: %d claims, %d clears; want 3, 1", store.claimCalls, store.clearCalls)
	}
}

func TestTOTPVerifyWithoutAFactorIsAPlainRejection(t *testing.T) {
	// No totp_secrets row means no budget to claim against either; that
	// must stay "code rejected", not turn into "locked".
	store := &fakeTOTPStore{locked: true}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)

	if ok, err := tp.Verify(context.Background(), uuid.New(), "123456"); ok || err != nil {
		t.Fatalf("Verify without a factor = %v, %v; want false, nil", ok, err)
	}
	if ok, err := tp.VerifyRecovery(context.Background(), uuid.New(), "aaaaa-bbbbb-ccccc"); ok || err != nil {
		t.Fatalf("VerifyRecovery without a factor = %v, %v; want false, nil", ok, err)
	}
}

func TestTOTPVerifyPropagatesClaimError(t *testing.T) {
	row, code := sealedConfirmedSecret(t)
	sentinel := errors.New("store unavailable")
	store := &fakeTOTPStore{hasSecret: true, secret: row, setLastStepOK: true, claimErr: sentinel}
	tp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)

	if ok, err := tp.Verify(context.Background(), uuid.New(), code); ok || !errors.Is(err, sentinel) {
		t.Fatalf("Verify = %v, %v; want the store's error", ok, err)
	}
}
