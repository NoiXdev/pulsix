package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func newLockFixture(t *testing.T) (context.Context, *gen.Queries, *auth.Sessions, uuid.UUID, uuid.UUID, string) {
	t.Helper()
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	s := auth.NewSessions(q, time.Now)
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "lock@example.test", DisplayName: "U"}); err != nil {
		t.Fatal(err)
	}
	id, cookie, err := s.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, q, s, uid, id, cookie
}

func TestLookupCarriesLockAndExpiry(t *testing.T) {
	ctx, q, s, _, id, cookie := newLockFixture(t)

	p, err := s.Lookup(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	if p.Locked {
		t.Fatal("fresh session reported locked")
	}
	row, err := q.GetSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ExpiresAt.Truncate(time.Second).Equal(row.ExpiresAt.Truncate(time.Second)) || p.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %v, want %v", p.ExpiresAt, row.ExpiresAt)
	}

	if n, err := q.LockSession(ctx, id); err != nil || n != 1 {
		t.Fatalf("LockSession = %d, %v", n, err)
	}
	p, err = s.Lookup(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Locked {
		t.Fatal("locked session not reported locked")
	}
	if !p.ExpiresAt.Truncate(time.Second).Equal(row.ExpiresAt.Truncate(time.Second)) {
		t.Fatalf("ExpiresAt = %v, want %v", p.ExpiresAt, row.ExpiresAt)
	}
}

// TestReplaceReportsTheStoredExpiry pins that a renewal answers the expiry
// the database keeps: Postgres stores microseconds, so a clock with
// nanoseconds (Linux) must not leak into the answer, or the renewal and
// /auth/me disagree about when the same session ends.
func TestReplaceReportsTheStoredExpiry(t *testing.T) {
	ctx, q, _, uid, id, _ := newLockFixture(t)
	// Now, so the renewed session is still live when it is looked up, with
	// nanoseconds Postgres cannot store.
	nanos := time.Now().UTC().Truncate(time.Second).Add(123456789)
	s := auth.NewSessions(q, func() time.Time { return nanos })

	renewed, err := s.Replace(ctx, uid, id, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Lookup(ctx, renewed.Cookie)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.Equal(p.ExpiresAt) {
		t.Fatalf("Replace answered %v, the stored expiry is %v", renewed.ExpiresAt, p.ExpiresAt)
	}
}

func TestStillValidRefusesLockedSession(t *testing.T) {
	ctx, q, s, _, id, _ := newLockFixture(t)

	if _, err := s.StillValid(ctx, id); err != nil {
		t.Fatalf("unlocked: %v", err)
	}
	if _, err := q.LockSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StillValid(ctx, id); !errors.Is(err, auth.ErrSessionLocked) {
		t.Fatalf("StillValid = %v, want ErrSessionLocked", err)
	}
	if n, err := q.UnlockSession(ctx, id); err != nil || n != 1 {
		t.Fatalf("UnlockSession = %d, %v", n, err)
	}
	if _, err := s.StillValid(ctx, id); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
}

func TestLockSessionOnlyAffectsLiveUnlockedSessions(t *testing.T) {
	ctx, q, s, _, id, _ := newLockFixture(t)

	if n, _ := q.LockSession(ctx, id); n != 1 {
		t.Fatalf("first lock affected %d rows", n)
	}
	if n, _ := q.LockSession(ctx, id); n != 0 {
		t.Fatalf("locking a locked session affected %d rows, want 0", n)
	}
	if n, _ := q.LockSession(ctx, uuid.Must(uuid.NewV7())); n != 0 {
		t.Fatalf("unknown session affected %d rows", n)
	}

	uid2 := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid2, Email: "lock2@example.test", DisplayName: "U"}); err != nil {
		t.Fatal(err)
	}
	revoked, _, _ := s.Create(ctx, uid2, true)
	if err := s.Revoke(ctx, revoked); err != nil {
		t.Fatal(err)
	}
	if n, _ := q.LockSession(ctx, revoked); n != 0 {
		t.Fatalf("revoked session affected %d rows", n)
	}
	expired := auth.NewSessions(q, func() time.Time { return time.Now().Add(-2 * auth.SessionTTL) })
	expID, _, _ := expired.Create(ctx, uid2, true)
	if n, _ := q.LockSession(ctx, expID); n != 0 {
		t.Fatalf("expired session affected %d rows", n)
	}
}

func TestClaimUnlockAttemptIsBoundedAndAtomic(t *testing.T) {
	ctx, q, _, _, id, _ := newLockFixture(t)

	if _, err := q.ClaimUnlockAttempt(ctx, id); err == nil {
		t.Fatal("claim on an unlocked session must return no row")
	}
	if _, err := q.LockSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	for want := int32(1); want <= 5; want++ {
		got, err := q.ClaimUnlockAttempt(ctx, id)
		if err != nil || got != want {
			t.Fatalf("claim %d = %d, %v", want, got, err)
		}
	}
	if _, err := q.ClaimUnlockAttempt(ctx, id); err == nil {
		t.Fatal("sixth claim must return no row")
	}

	// Concurrent claims never exceed the limit.
	if n, _ := q.UnlockSession(ctx, id); n != 1 {
		t.Fatal("unlock failed")
	}
	if _, err := q.LockSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() { _, err := q.ClaimUnlockAttempt(ctx, id); results <- err }()
	}
	ok := 0
	for i := 0; i < 20; i++ {
		if <-results == nil {
			ok++
		}
	}
	if ok != 5 {
		t.Fatalf("%d concurrent claims succeeded, want exactly 5", ok)
	}
}

func TestUnlockSessionResetsFailures(t *testing.T) {
	ctx, q, _, _, id, _ := newLockFixture(t)
	_, _ = q.LockSession(ctx, id)
	_, _ = q.ClaimUnlockAttempt(ctx, id)
	if n, err := q.UnlockSession(ctx, id); err != nil || n != 1 {
		t.Fatalf("UnlockSession = %d, %v", n, err)
	}
	_, _ = q.LockSession(ctx, id)
	got, err := q.ClaimUnlockAttempt(ctx, id)
	if err != nil || got != 1 {
		t.Fatalf("first claim after relock = %d, %v; want 1", got, err)
	}
}

func TestLockPINQueries(t *testing.T) {
	ctx, q, _, uid, _, _ := newLockFixture(t)

	row, err := q.GetLockPIN(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.LockPinHash != nil || row.LockPinPromptedAt.Valid {
		t.Fatalf("fresh user has PIN state: %+v", row)
	}

	if err := q.MarkLockPINPrompted(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, _ = q.GetLockPIN(ctx, uid)
	if row.LockPinHash != nil || !row.LockPinPromptedAt.Valid {
		t.Fatalf("after prompt: %+v", row)
	}

	if err := q.SetLockPIN(ctx, gen.SetLockPINParams{UserID: uid, LockPinHash: ptr("hash"), LockPinDigits: ptr(int16(4))}); err != nil {
		t.Fatal(err)
	}
	row, _ = q.GetLockPIN(ctx, uid)
	if row.LockPinHash == nil || *row.LockPinHash != "hash" || !row.LockPinPromptedAt.Valid ||
		row.LockPinDigits == nil || *row.LockPinDigits != 4 {
		t.Fatalf("after set: %+v", row)
	}

	if err := q.RemoveLockPIN(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, _ = q.GetLockPIN(ctx, uid)
	if row.LockPinHash != nil || row.LockPinDigits != nil || !row.LockPinPromptedAt.Valid {
		t.Fatalf("after remove (prompted kept): %+v", row)
	}

	_ = q.SetLockPIN(ctx, gen.SetLockPINParams{UserID: uid, LockPinHash: ptr("hash"), LockPinDigits: ptr(int16(4))})
	if err := q.ClearLockPIN(ctx, uid); err != nil {
		t.Fatal(err)
	}
	row, _ = q.GetLockPIN(ctx, uid)
	if row.LockPinHash != nil || row.LockPinDigits != nil || row.LockPinPromptedAt.Valid {
		t.Fatalf("after clear: %+v", row)
	}
}

func ptr[T any](v T) *T { return &v }
