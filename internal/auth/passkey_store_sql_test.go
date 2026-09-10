package auth_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func randomCredentialID(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func seedUser(t *testing.T, q *gen.Queries, email string) uuid.UUID {
	t.Helper()
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(context.Background(), gen.CreateUserParams{ID: uid, Email: email, DisplayName: email}); err != nil {
		t.Fatal(err)
	}
	return uid
}

func TestSQLPasskeyStoreCredentials(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := auth.NewSQLPasskeyStore(q)

	uid := seedUser(t, q, "passkey@example.test")
	credID := randomCredentialID(t)
	cred := auth.Credential{
		ID: uuid.Must(uuid.NewV7()), CredentialID: credID, PublicKey: []byte("pubkey"), Attestation: "none",
		Transports: []string{"internal", "hybrid"}, SignCount: 3, BackupEligible: true,
	}
	if err := store.AddCredential(ctx, uid, "YubiKey", cred); err != nil {
		t.Fatalf("AddCredential: %v", err)
	}

	list, err := store.ListCredentials(ctx, uid)
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListCredentials returned %d rows, want 1", len(list))
	}
	got := list[0]
	if got.Name != "YubiKey" || string(got.CredentialID) != string(credID) || got.Attestation != "none" ||
		got.SignCount != 3 || !got.BackupEligible || len(got.Transports) != 2 {
		t.Fatalf("ListCredentials row = %+v", got)
	}
	if got.LastUsedAt != nil {
		t.Fatalf("LastUsedAt should be unset before first login, got %v", got.LastUsedAt)
	}

	owner, err := store.FindUserByCredentialID(ctx, credID)
	if err != nil || owner != uid {
		t.Fatalf("FindUserByCredentialID = %v, %v, want %v", owner, err, uid)
	}

	if _, err := store.FindUserByCredentialID(ctx, randomCredentialID(t)); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("FindUserByCredentialID(unknown) err = %v, want ErrNotFound", err)
	}

	if err := store.UpdateCredentialCounter(ctx, credID, 42); err != nil {
		t.Fatalf("UpdateCredentialCounter: %v", err)
	}
	list, err = store.ListCredentials(ctx, uid)
	if err != nil || len(list) != 1 || list[0].SignCount != 42 {
		t.Fatalf("ListCredentials after counter update = %+v, %v", list, err)
	}
	if list[0].LastUsedAt == nil {
		t.Fatal("LastUsedAt should be set after UpdateCredentialCounter")
	}
}

func TestSQLPasskeyStoreDeleteCredential(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := auth.NewSQLPasskeyStore(q)

	uid := seedUser(t, q, "delete@example.test")
	other := seedUser(t, q, "other@example.test")
	credID := randomCredentialID(t)
	cred := auth.Credential{ID: uuid.Must(uuid.NewV7()), CredentialID: credID, PublicKey: []byte("pk"), Attestation: "none"}
	if err := store.AddCredential(ctx, uid, "Key", cred); err != nil {
		t.Fatal(err)
	}

	// A different user's id must not delete uid's credential.
	if err := store.DeleteCredential(ctx, other, cred.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("DeleteCredential(wrong owner) err = %v, want ErrNotFound", err)
	}
	list, _ := store.ListCredentials(ctx, uid)
	if len(list) != 1 {
		t.Fatal("credential was deleted by the wrong owner")
	}

	if err := store.DeleteCredential(ctx, uid, cred.ID); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	list, _ = store.ListCredentials(ctx, uid)
	if len(list) != 0 {
		t.Fatal("credential still present after delete")
	}

	if err := store.DeleteCredential(ctx, uid, cred.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("DeleteCredential(already gone) err = %v, want ErrNotFound", err)
	}
}

func TestSQLPasskeyStoreCeremonyIsSingleUse(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := auth.NewSQLPasskeyStore(q)

	uid := seedUser(t, q, "ceremony@example.test")
	id := uuid.Must(uuid.NewV7())
	data := []byte(`{"challenge":"abc"}`)
	if err := store.PutCeremony(ctx, id, "register", &uid, data, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("PutCeremony: %v", err)
	}

	kind, gotUser, gotData, err := store.TakeCeremony(ctx, id)
	if err != nil {
		t.Fatalf("TakeCeremony (first): %v", err)
	}
	if kind != "register" || gotUser == nil || *gotUser != uid || string(gotData) != string(data) {
		t.Fatalf("TakeCeremony = kind=%q user=%v data=%s", kind, gotUser, gotData)
	}

	if _, _, _, err := store.TakeCeremony(ctx, id); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("TakeCeremony (second): err = %v, want ErrNotFound", err)
	}
}

func TestSQLPasskeyStoreCeremonyExpires(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := auth.NewSQLPasskeyStore(q)

	id := uuid.Must(uuid.NewV7())
	if err := store.PutCeremony(ctx, id, "login", nil, []byte(`{}`), time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("PutCeremony: %v", err)
	}

	if _, _, _, err := store.TakeCeremony(ctx, id); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("TakeCeremony(expired) err = %v, want ErrNotFound", err)
	}
}

func TestSQLPasskeyStoreTakeCeremonyUnknownID(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := auth.NewSQLPasskeyStore(q)

	if _, _, _, err := store.TakeCeremony(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("TakeCeremony(unknown) err = %v, want ErrNotFound", err)
	}
}

// TestSQLPasskeyStoreQueriesFailWithCanceledContext exercises every
// adapter method's generic (non-pgx.ErrNoRows) error path the same way
// internal/db/errors_test.go does: an already-canceled context makes pgx
// fail every query, without needing to fabricate a real database outage.
func TestSQLPasskeyStoreQueriesFailWithCanceledContext(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	store := auth.NewSQLPasskeyStore(q)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.ListCredentials(ctx, uuid.Must(uuid.NewV7())); err == nil {
		t.Error("ListCredentials with a canceled context: want error")
	}
	if err := store.DeleteCredential(ctx, uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())); err == nil {
		t.Error("DeleteCredential with a canceled context: want error")
	}
	if _, err := store.FindUserByCredentialID(ctx, []byte("x")); err == nil {
		t.Error("FindUserByCredentialID with a canceled context: want error")
	}
	if _, _, _, err := store.TakeCeremony(ctx, uuid.Must(uuid.NewV7())); err == nil {
		t.Error("TakeCeremony with a canceled context: want error")
	}
}

// TestSQLPasskeyStoreLoginCeremonyHasNoUser exercises the nil *uuid.UUID
// path end to end through PutCeremony/TakeCeremony's pgtype.UUID
// conversion: a login ceremony is not bound to a user until the assertion
// resolves one.
func TestSQLPasskeyStoreLoginCeremonyHasNoUser(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	store := auth.NewSQLPasskeyStore(q)

	id := uuid.Must(uuid.NewV7())
	if err := store.PutCeremony(ctx, id, "login", nil, []byte(`{}`), time.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("PutCeremony: %v", err)
	}
	_, gotUser, _, err := store.TakeCeremony(ctx, id)
	if err != nil {
		t.Fatalf("TakeCeremony: %v", err)
	}
	if gotUser != nil {
		t.Fatalf("TakeCeremony user = %v, want nil", gotUser)
	}
}
