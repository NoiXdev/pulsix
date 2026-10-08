package auth_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestSQLTOTPStoreConfirmEnrolIsAtomic asserts Finding 3 directly against
// real SQL: if any write inside ConfirmEnrol fails, none of it must stick —
// confirmed_at must stay unset and no recovery code must be stored, even
// though ConfirmTOTP and ReplaceRecoveryCodes (the first two statements)
// would have succeeded on their own.
//
// The failure is a genuine database error, not an injected one: passing the
// same hash twice makes the second InsertRecoveryCode violate
// recovery_codes' (user_id, code_hash) primary key. That happens inside the
// transaction, after ConfirmTOTP has already run — exactly the "failure
// partway through" scenario the finding describes — so a rollback is the
// only thing that can make the assertions below pass.
func TestSQLTOTPStoreConfirmEnrolIsAtomic(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "t@example.test", DisplayName: "T"}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertTOTPSecret(ctx, gen.UpsertTOTPSecretParams{UserID: uid, SecretSealed: []byte("sealed")}); err != nil {
		t.Fatal(err)
	}

	store := auth.NewSQLTOTPStore(d)
	dupHash := []byte("duplicate-hash")
	hashes := [][]byte{dupHash, dupHash, []byte("other-hash")}
	if err := store.ConfirmEnrol(ctx, uid, hashes); err == nil {
		t.Fatal("ConfirmEnrol with a colliding hash succeeded, want a primary-key violation")
	}

	row, err := q.GetTOTPSecret(ctx, uid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.ConfirmedAt.Valid {
		t.Fatal("confirmed_at was set despite the transaction failing — ConfirmTOTP was not rolled back")
	}
	if n, err := q.UseRecoveryCode(ctx, gen.UseRecoveryCodeParams{UserID: uid, CodeHash: dupHash}); err != nil || n != 0 {
		t.Fatalf("a recovery code from the failed transaction is usable: n=%d, err=%v", n, err)
	}
	if n, err := q.UseRecoveryCode(ctx, gen.UseRecoveryCodeParams{UserID: uid, CodeHash: []byte("other-hash")}); err != nil || n != 0 {
		t.Fatalf("a recovery code from the failed transaction is usable: n=%d, err=%v", n, err)
	}
}

// ReplaceRecoveryCodesWith has to be all-or-nothing for the same reason
// ConfirmEnrol does, and the failure is the dangerous direction: a swap
// that deleted the old set and then failed to insert the new one would
// leave an account with no way back in at all — strictly worse than the
// state it was asked to improve.
func TestSQLTOTPStoreReplaceRecoveryCodesWithIsAtomic(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "swap@example.test", DisplayName: "S"}); err != nil {
		t.Fatal(err)
	}
	store := auth.NewSQLTOTPStore(d)

	original := [][]byte{[]byte("first-code"), []byte("second-code")}
	if err := store.ReplaceRecoveryCodesWith(ctx, uid, original); err != nil {
		t.Fatalf("seeding the first set: %v", err)
	}
	if n, err := store.CountUnusedRecoveryCodes(ctx, uid); err != nil || n != 2 {
		t.Fatalf("after the first set: n=%d err=%v, want 2", n, err)
	}

	// A set that cannot be inserted: the duplicate collides on the
	// primary key halfway through.
	dup := []byte("same-hash")
	if err := store.ReplaceRecoveryCodesWith(ctx, uid, [][]byte{dup, dup}); err == nil {
		t.Fatal("a colliding set was accepted, want a primary-key violation")
	}
	if n, err := store.CountUnusedRecoveryCodes(ctx, uid); err != nil || n != 2 {
		t.Fatalf("after the failed swap: n=%d err=%v — the original set must still stand", n, err)
	}
	if n, err := q.UseRecoveryCode(ctx, gen.UseRecoveryCodeParams{UserID: uid, CodeHash: original[0]}); err != nil || n != 1 {
		t.Fatalf("an original code after the failed swap: n=%d err=%v, want it still redeemable", n, err)
	}

	// And the successful swap really does retire the old set.
	if err := store.ReplaceRecoveryCodesWith(ctx, uid, [][]byte{[]byte("third-code")}); err != nil {
		t.Fatalf("second swap: %v", err)
	}
	if n, err := q.UseRecoveryCode(ctx, gen.UseRecoveryCodeParams{UserID: uid, CodeHash: original[1]}); err != nil || n != 0 {
		t.Fatalf("a code from the replaced set is still redeemable: n=%d err=%v", n, err)
	}
}

// CountUnusedRecoveryCodes counts what can still be redeemed, not what
// rows exist: spending a code stamps used_at rather than deleting it.
func TestSQLTOTPStoreCountUnusedRecoveryCodesExcludesSpentOnes(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "count@example.test", DisplayName: "C"}); err != nil {
		t.Fatal(err)
	}
	store := auth.NewSQLTOTPStore(d)
	codes := [][]byte{[]byte("code-a"), []byte("code-b"), []byte("code-c")}
	if err := store.ReplaceRecoveryCodesWith(ctx, uid, codes); err != nil {
		t.Fatal(err)
	}

	if _, err := q.UseRecoveryCode(ctx, gen.UseRecoveryCodeParams{UserID: uid, CodeHash: codes[0]}); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CountUnusedRecoveryCodes(ctx, uid); err != nil || n != 2 {
		t.Fatalf("after spending one: n=%d err=%v, want 2 — the spent row still exists", n, err)
	}
}
