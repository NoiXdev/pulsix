package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func lastSignIn(t *testing.T, d *db.DB, uid uuid.UUID) *time.Time {
	t.Helper()
	var at *time.Time
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT last_sign_in_at FROM users WHERE id = $1`, uid).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestLastSignInStampedOnCompletion(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	s := auth.NewSessions(q, time.Now)
	newUser := func(email string) uuid.UUID {
		uid := uuid.Must(uuid.NewV7())
		if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: email, DisplayName: "U"}); err != nil {
			t.Fatal(err)
		}
		return uid
	}

	t.Run("a password session is not a completed sign-in", func(t *testing.T) {
		uid := newUser("pw@example.test")
		if _, _, err := s.Create(ctx, uid, false); err != nil {
			t.Fatal(err)
		}
		if at := lastSignIn(t, d, uid); at != nil {
			t.Fatalf("last_sign_in_at = %v after an incomplete session, want NULL", at)
		}
	})

	t.Run("completing the second factor stamps once", func(t *testing.T) {
		uid := newUser("mfa@example.test")
		id, _, err := s.Create(ctx, uid, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkMFAComplete(ctx, id); err != nil {
			t.Fatal(err)
		}
		first := lastSignIn(t, d, uid)
		if first == nil {
			t.Fatal("last_sign_in_at is NULL after MarkMFAComplete")
		}
		// A step-up re-proves the factor on an already complete session
		// and must not look like a new sign-in.
		time.Sleep(20 * time.Millisecond)
		if _, err := s.MarkMFAComplete(ctx, id); err != nil {
			t.Fatal(err)
		}
		second := lastSignIn(t, d, uid)
		if second == nil || !second.Equal(*first) {
			t.Fatalf("last_sign_in_at moved on a step-up: %v -> %v", first, second)
		}
	})

	t.Run("a session created complete stamps", func(t *testing.T) {
		uid := newUser("passkey@example.test")
		if _, _, err := s.Create(ctx, uid, true); err != nil {
			t.Fatal(err)
		}
		if at := lastSignIn(t, d, uid); at == nil {
			t.Fatal("last_sign_in_at is NULL after Create(…, true)")
		}
	})
}

func TestLookupCarriesMustChangePassword(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	s := auth.NewSessions(q, time.Now)
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "must@example.test", DisplayName: "M"}); err != nil {
		t.Fatal(err)
	}
	_, cookie, err := s.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Lookup(ctx, cookie)
	if err != nil || p.MustChangePassword {
		t.Fatalf("default lookup = %+v, %v; want MustChangePassword false", p, err)
	}
	if _, err := d.Pool().Exec(ctx, `UPDATE users SET must_change_password = true WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	p, err = s.Lookup(ctx, cookie)
	if err != nil || !p.MustChangePassword {
		t.Fatalf("lookup = %+v, %v; want MustChangePassword true", p, err)
	}
}
