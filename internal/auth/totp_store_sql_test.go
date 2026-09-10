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

	store := auth.NewSQLTOTPStore(d.Pool())
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
