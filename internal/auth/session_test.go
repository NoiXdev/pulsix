package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func TestSessionCreateLookupRevoke(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "s@example.test", DisplayName: "S"}); err != nil {
		t.Fatal(err)
	}
	s := auth.NewSessions(q, time.Now)
	id, cookie, err := s.Create(ctx, uid, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Lookup(ctx, cookie)
	if err != nil || p.UserID != uid || p.SessionID != id || p.MFAComplete {
		t.Fatalf("lookup = %+v, %v", p, err)
	}
	if _, err := s.Lookup(ctx, cookie+"x"); err == nil {
		t.Fatal("tampered cookie accepted")
	}
	if err := s.Revoke(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, cookie); err == nil {
		t.Fatal("revoked session accepted")
	}
}

func TestSessionMarkMFAComplete(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "mfa@example.test", DisplayName: "MFA"}); err != nil {
		t.Fatal(err)
	}
	s := auth.NewSessions(q, time.Now)
	id, cookie, err := s.Create(ctx, uid, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Lookup(ctx, cookie)
	if err != nil || p.MFAComplete {
		t.Fatalf("before MarkMFAComplete: %+v, %v", p, err)
	}
	if err := s.MarkMFAComplete(ctx, id); err != nil {
		t.Fatal(err)
	}
	p, err = s.Lookup(ctx, cookie)
	if err != nil || !p.MFAComplete {
		t.Fatalf("after MarkMFAComplete: %+v, %v", p, err)
	}
}

// TestSessionLookupRejectsDisabledUser is the query-level backstop half of
// Finding 2's fix (Task 10 fix round 1): GetSession's own WHERE clause now
// excludes a disabled user's row (auth.sql), so a session stops resolving
// the instant the owning account is disabled — even one this test never
// explicitly revokes — not only through DeactivateUser's explicit
// RevokeSessionsForUser call. This is the backstop that makes it
// impossible for a future caller of Sessions.Lookup to forget the check.
func TestSessionLookupRejectsDisabledUser(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "disabled-session@example.test", DisplayName: "D"}); err != nil {
		t.Fatal(err)
	}
	s := auth.NewSessions(q, time.Now)
	_, cookie, err := s.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, cookie); err != nil {
		t.Fatalf("lookup before disabling: %v", err)
	}

	if err := q.DisableUser(ctx, gen.DisableUserParams{ID: uid, DisabledAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, cookie); err == nil {
		t.Fatal("session for a disabled user was accepted")
	}
}

// TestSessionExpiry drives session expiry through the public API by
// injecting a clock set in the past, rather than relying only on the SQL
// layer's own "expires_at > now()" clause, which previously had no test
// coverage at all (Finding 4, fix round 1).
func TestSessionExpiry(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "expired@example.test", DisplayName: "Expired"}); err != nil {
		t.Fatal(err)
	}

	// A session created with a clock this far in the past has already
	// expired by the time Create sets ExpiresAt = now() + SessionTTL,
	// since SessionTTL (12h) is much shorter than 48h.
	past := func() time.Time { return time.Now().Add(-48 * time.Hour) }
	s := auth.NewSessions(q, past)
	_, cookie, err := s.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup(ctx, cookie); err == nil {
		t.Fatal("expired session accepted")
	}
}
