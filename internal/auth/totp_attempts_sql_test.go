package auth_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

func enrolledUser(t *testing.T, q *gen.Queries) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: uid.String() + "@example.test", DisplayName: "T"}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertTOTPSecret(ctx, gen.UpsertTOTPSecretParams{UserID: uid, SecretSealed: []byte("sealed")}); err != nil {
		t.Fatal(err)
	}
	return uid
}

// TestSQLTOTPStoreFactorAttemptsLockWithGrowingBackoff is backlog 7.26's
// budget against real SQL: five attempts are free, the fifth starts a
// one-minute lock, every further one that gets through doubles it, and
// clearing (what a success does) gives the whole budget back.
func TestSQLTOTPStoreFactorAttemptsLockWithGrowingBackoff(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	uid := enrolledUser(t, gen.New(d.Pool()))
	store := auth.NewSQLTOTPStore(d)
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	claim := func(at time.Time) bool {
		t.Helper()
		ok, err := store.ClaimFactorAttempt(ctx, uid, at)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for i := range 5 {
		if !claim(t0) {
			t.Fatalf("attempt %d refused; the first five are free", i+1)
		}
	}
	if claim(t0.Add(59 * time.Second)) {
		t.Fatal("the sixth attempt inside the first minute got through")
	}
	if !claim(t0.Add(time.Minute)) {
		t.Fatal("the lock outlived its minute")
	}
	if claim(t0.Add(time.Minute + 119*time.Second)) {
		t.Fatal("the second lock was not two minutes")
	}
	if !claim(t0.Add(3 * time.Minute)) {
		t.Fatal("the second lock outlived its two minutes")
	}

	if err := store.ClearFactorAttempts(ctx, uid); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if !claim(t0.Add(3 * time.Minute)) {
			t.Fatalf("attempt %d after a success refused; a success gives the budget back", i+1)
		}
	}
}

// TestSQLTOTPStoreFactorAttemptsCapAtAnHour: however many attempts an
// attacker has spent, one lock never exceeds an hour, so the account's
// owner is never locked out for longer than that.
func TestSQLTOTPStoreFactorAttemptsCapAtAnHour(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := enrolledUser(t, q)
	store := auth.NewSQLTOTPStore(d)
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	for i := range 2000 {
		ok, err := store.ClaimFactorAttempt(ctx, uid, at)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		if !ok {
			t.Fatalf("attempt %d refused at its lock's end", i+1)
		}
		row, err := q.GetTOTPSecret(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if row.FactorLockedUntil.Valid {
			if lock := row.FactorLockedUntil.Time.Sub(at); lock > time.Hour {
				t.Fatalf("attempt %d locked the account for %v", i+1, lock)
			}
			at = row.FactorLockedUntil.Time
		}
	}
}

// TestSQLTOTPStoreFactorAttemptsHoldUnderConcurrency: the budget is spent
// by the claim itself, under the row's lock, so a burst of parallel
// guesses from many addresses gets exactly the five free attempts.
func TestSQLTOTPStoreFactorAttemptsHoldUnderConcurrency(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	uid := enrolledUser(t, gen.New(d.Pool()))
	store := auth.NewSQLTOTPStore(d)
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	var granted atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.ClaimFactorAttempt(ctx, uid, at)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := granted.Load(); got != 5 {
		t.Fatalf("%d of 40 parallel attempts got through; want 5", got)
	}
}

func TestSQLTOTPStoreFactorAttemptsNeedAFactor(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	q := gen.New(d.Pool())
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "n@example.test", DisplayName: "N"}); err != nil {
		t.Fatal(err)
	}
	ok, err := auth.NewSQLTOTPStore(d).ClaimFactorAttempt(ctx, uid, time.Now())
	if err != nil || ok {
		t.Fatalf("ClaimFactorAttempt without a factor = %v, %v; want false, nil", ok, err)
	}
}
