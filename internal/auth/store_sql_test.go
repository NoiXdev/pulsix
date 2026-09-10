package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestSQLStoreGetUser(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	hash := "$argon2id$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "store@example.test", DisplayName: "Store", PasswordHash: &hash}); err != nil {
		t.Fatal(err)
	}
	store := auth.NewSQLStore(q)

	byEmail, err := store.GetUserByEmail(ctx, "store@example.test")
	if err != nil || byEmail.ID != uid || byEmail.PasswordHash == nil || *byEmail.PasswordHash != hash {
		t.Fatalf("GetUserByEmail = %+v, %v", byEmail, err)
	}

	byID, err := store.GetUserByID(ctx, uid)
	if err != nil || byID.Email != "store@example.test" {
		t.Fatalf("GetUserByID = %+v, %v", byID, err)
	}

	if _, err := store.GetUserByEmail(ctx, "missing@example.test"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetUserByEmail(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := store.GetUserByID(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("GetUserByID(missing) err = %v, want ErrNotFound", err)
	}
}
