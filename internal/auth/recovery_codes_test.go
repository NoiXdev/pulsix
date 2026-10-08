package auth_test

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// The invariant that matters: the codes handed to the user must be the
// codes that were stored. They are shown once and never again, so a
// mismatch between what is displayed and what is hashed into the table
// would not surface until someone tried to use one — by which time the
// authenticator is already gone and this was the way back.
func TestRegenerateRecoveryCodesStoresTheCodesItReturns(t *testing.T) {
	store := &fakeTOTPStore{}
	totp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)

	codes, err := totp.RegenerateRecoveryCodes(ctx, uuid.New())
	if err != nil {
		t.Fatalf("RegenerateRecoveryCodes: %v", err)
	}
	if len(store.replaceWithCalls) != 1 {
		t.Fatalf("store wrote %d sets, want exactly one", len(store.replaceWithCalls))
	}
	stored := store.replaceWithCalls[0]
	if len(stored) != len(codes) {
		t.Fatalf("stored %d hashes for %d codes", len(stored), len(codes))
	}
	for i, c := range codes {
		sum := sha256.Sum256([]byte(c))
		if string(stored[i]) != string(sum[:]) {
			t.Fatalf("code %d does not hash to what was stored — the set shown and the set saved differ", i)
		}
	}
}

// A set must be a set: every code distinct, and shaped the way the
// enrolment path's codes are, since both doors hand out the same thing.
func TestRegenerateRecoveryCodesMintsADistinctWellFormedSet(t *testing.T) {
	store := &fakeTOTPStore{}
	totp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)

	codes, err := totp.RegenerateRecoveryCodes(ctx, uuid.New())
	if err != nil {
		t.Fatalf("RegenerateRecoveryCodes: %v", err)
	}
	seen := make(map[string]bool, len(codes))
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate code %q in one set", c)
		}
		seen[c] = true
		parts := strings.Split(c, "-")
		if len(parts) != 3 || len(parts[0]) != 5 || len(parts[1]) != 5 || len(parts[2]) != 5 {
			t.Fatalf("code %q is not the 5-5-5 shape enrolment hands out", c)
		}
	}
}

func TestRecoveryCodesRemainingReportsTheStoresCount(t *testing.T) {
	store := &fakeTOTPStore{unusedRecovery: 3}
	totp := auth.NewTOTP(store, testTOTPBox(), "Pulsix Test", testTOTPNow)

	n, err := totp.RecoveryCodesRemaining(ctx, uuid.New())
	if err != nil {
		t.Fatalf("RecoveryCodesRemaining: %v", err)
	}
	if n != 3 {
		t.Fatalf("RecoveryCodesRemaining = %d, want the store's 3", n)
	}
}
