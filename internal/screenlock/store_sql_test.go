package screenlock_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/screenlock"
)

type sqlFixture struct {
	q      *gen.Queries
	svc    *screenlock.Service
	sess   *auth.Sessions
	uid    uuid.UUID
	sid    uuid.UUID
	cookie string
	pool   *pgxpool.Pool
}

func newSQLFixture(t *testing.T) sqlFixture {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	sess := auth.NewSessions(q, time.Now)
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Email: "pin@example.test", DisplayName: "U"}); err != nil {
		t.Fatal(err)
	}
	sid, cookie, err := sess.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	return sqlFixture{q, screenlock.NewService(screenlock.NewSQLStore(q)), sess, uid, sid, cookie, d.Pool()}
}

func TestSQLStoreEndToEnd(t *testing.T) {
	f := newSQLFixture(t)

	st, err := f.svc.Status(ctx, f.uid)
	if err != nil || !st.Prompt || st.PINSet {
		t.Fatalf("fresh Status = %+v, %v", st, err)
	}
	if err := f.svc.Lock(ctx, f.uid, f.sid); !errors.Is(err, screenlock.ErrNoPIN) {
		t.Fatalf("Lock without PIN = %v", err)
	}
	if _, err := f.svc.Unlock(ctx, f.uid, f.sid, "1234"); !errors.Is(err, screenlock.ErrNotLocked) {
		t.Fatalf("Unlock unlocked = %v", err)
	}
	if err := f.svc.SetPIN(ctx, f.uid, "1234"); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.svc.Status(ctx, f.uid); !st.PINSet || st.Prompt {
		t.Fatalf("Status after SetPIN = %+v", st)
	}
	if err := f.svc.Lock(ctx, f.uid, f.sid); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Lock(ctx, f.uid, f.sid); err != nil {
		t.Fatalf("second Lock = %v", err)
	}
	if p, err := f.sess.Lookup(ctx, f.cookie); err != nil || !p.Locked {
		t.Fatalf("Lookup = %+v, %v", p, err)
	}
	// 3 wrong, 1 right, lock again: five attempts again.
	for range 3 {
		if _, err := f.svc.Unlock(ctx, f.uid, f.sid, "0000"); !errors.Is(err, screenlock.ErrWrongPIN) {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Unlock(ctx, f.uid, f.sid, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Lock(ctx, f.uid, f.sid); err != nil {
		t.Fatal(err)
	}
	for want := 4; want >= 1; want-- {
		left, err := f.svc.Unlock(ctx, f.uid, f.sid, "0000")
		if !errors.Is(err, screenlock.ErrWrongPIN) || left != want {
			t.Fatalf("left %d err %v want %d", left, err, want)
		}
	}
}

func TestSQLStoreRemoveAndSkipKeepPrompted(t *testing.T) {
	f := newSQLFixture(t)
	if err := f.svc.SetPIN(ctx, f.uid, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemovePIN(ctx, f.uid); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.svc.Status(ctx, f.uid); st.PINSet || st.Prompt {
		t.Fatalf("after remove = %+v", st)
	}
	// Admin reset re-offers the step.
	if err := f.q.ClearLockPIN(ctx, f.uid); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.svc.Status(ctx, f.uid); !st.Prompt {
		t.Fatalf("after reset = %+v", st)
	}
	if err := f.svc.SkipPrompt(ctx, f.uid); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.svc.Status(ctx, f.uid); st.Prompt || st.PINSet {
		t.Fatalf("after skip = %+v", st)
	}
}

func TestSQLStoreExhaustionRevokes(t *testing.T) {
	f := newSQLFixture(t)
	if err := f.svc.SetPIN(ctx, f.uid, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Lock(ctx, f.uid, f.sid); err != nil {
		t.Fatal(err)
	}
	var last error
	for range screenlock.MaxUnlockFailures {
		_, last = f.svc.Unlock(ctx, f.uid, f.sid, "0000")
	}
	if !errors.Is(last, screenlock.ErrExhausted) {
		t.Fatalf("fifth = %v", last)
	}
	if _, err := f.sess.Lookup(ctx, f.cookie); err == nil {
		t.Fatal("session still valid after exhaustion")
	}
	if _, err := f.svc.Unlock(ctx, f.uid, f.sid, "1234"); !errors.Is(err, screenlock.ErrNotFound) {
		t.Fatalf("Unlock on revoked = %v", err)
	}
	if err := f.svc.Lock(ctx, f.uid, f.sid); !errors.Is(err, screenlock.ErrNotFound) {
		t.Fatalf("Lock on revoked = %v", err)
	}
}

func TestSQLStoreExhaustedButUnrevokedIsRevoked(t *testing.T) {
	f := newSQLFixture(t)
	if err := f.svc.SetPIN(ctx, f.uid, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Lock(ctx, f.uid, f.sid); err != nil {
		t.Fatal(err)
	}
	for range screenlock.MaxUnlockFailures {
		if _, err := f.q.ClaimUnlockAttempt(ctx, f.sid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Unlock(ctx, f.uid, f.sid, "1234"); !errors.Is(err, screenlock.ErrExhausted) {
		t.Fatalf("Unlock = %v", err)
	}
	if _, err := f.sess.Lookup(ctx, f.cookie); err == nil {
		t.Fatal("session still valid")
	}
}

func TestUnlockClaimsAttemptsAtomically(t *testing.T) {
	f := newSQLFixture(t)
	if err := f.svc.SetPIN(ctx, f.uid, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Lock(ctx, f.uid, f.sid); err != nil {
		t.Fatal(err)
	}
	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.Unlock(ctx, f.uid, f.sid, "0000")
		}()
	}
	wg.Wait()
	wrong, exhausted := 0, 0
	for _, err := range errs {
		switch {
		case errors.Is(err, screenlock.ErrWrongPIN):
			wrong++
		case errors.Is(err, screenlock.ErrExhausted), errors.Is(err, screenlock.ErrNotFound):
			exhausted++
		default:
			t.Fatalf("unexpected result %v", err)
		}
	}
	// Exactly five claims succeed: four wrong, one exhausting; the rest find
	// the session spent.
	if wrong != 4 {
		t.Fatalf("wrong = %d, want 4 (errs %v)", wrong, errs)
	}
	if _, err := f.sess.Lookup(ctx, f.cookie); err == nil {
		t.Fatal("session still valid")
	}
	if _, err := f.q.ClaimUnlockAttempt(ctx, f.sid); err == nil {
		t.Fatal("a sixth claim succeeded")
	}
}

func TestSQLStoreSurfacesDatabaseErrors(t *testing.T) {
	f := newSQLFixture(t)
	dead, cancel := context.WithCancel(ctx)
	cancel()
	st := screenlock.NewSQLStore(f.q)
	if _, _, _, err := st.PINState(dead, f.uid); err == nil {
		t.Error("PINState")
	}
	if _, _, err := st.ClaimAttempt(dead, f.sid); err == nil {
		t.Error("ClaimAttempt")
	}
	if _, _, err := st.SessionState(dead, f.sid); err == nil {
		t.Error("SessionState")
	}
	if _, err := st.LockSession(dead, f.sid); err == nil {
		t.Error("LockSession")
	}
	if err := st.UnlockSession(dead, f.sid); err == nil {
		t.Error("UnlockSession")
	}
	if err := st.RevokeSession(dead, f.sid); err == nil {
		t.Error("RevokeSession")
	}
	if err := st.SetPIN(dead, f.uid, "x", 4); err == nil {
		t.Error("SetPIN")
	}
}

func TestSQLStoreRecordsDigitsAndFlagsLegacy(t *testing.T) {
	f := newSQLFixture(t)
	if err := f.svc.SetPIN(ctx, f.uid, "0427"); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.svc.Status(ctx, f.uid); st != (screenlock.Status{PINSet: true}) {
		t.Fatalf("Status after SetPIN = %+v", st)
	}
	// Simulate the previous release: a 6-digit hash with no recorded length.
	hash, err := auth.HashPassword("123456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE users SET lock_pin_hash = $2, lock_pin_digits = NULL WHERE id = $1`, f.uid, hash); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.svc.Status(ctx, f.uid); st != (screenlock.Status{PINSet: true, Legacy: true}) {
		t.Fatalf("Status of older PIN = %+v", st)
	}
	if err := f.svc.Lock(ctx, f.uid, f.sid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Unlock(ctx, f.uid, f.sid, "123456"); err != nil {
		t.Fatalf("older 6-digit PIN does not unlock: %v", err)
	}
	if err := f.svc.RemovePIN(ctx, f.uid); err != nil {
		t.Fatal(err)
	}
	var digits *int16
	if err := f.pool.QueryRow(ctx, `SELECT lock_pin_digits FROM users WHERE id = $1`, f.uid).Scan(&digits); err != nil || digits != nil {
		t.Fatalf("digits after remove = %v, %v", digits, err)
	}
}
