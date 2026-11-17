package screenlock_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/screenlock"
)

type fakeStore struct {
	hash       string
	prompted   bool
	live       bool
	locked     bool
	failures   int
	revoked    bool
	err        error  // returned by every method when set
	denyClaims int    // next ClaimAttempt calls that miss although a claim would succeed
	failOn     string // method name that fails alone, when set
}

func newFake() *fakeStore { return &fakeStore{live: true} }

var errBoom = errors.New("boom")

// fail reports errBoom for the method named by failOn.
func (f *fakeStore) fail(m string) error {
	if f.failOn == m {
		return errBoom
	}
	return nil
}

func (f *fakeStore) PINState(context.Context, uuid.UUID) (string, bool, error) {
	return f.hash, f.prompted, errors.Join(f.err, f.fail("PINState"))
}
func (f *fakeStore) SetPIN(_ context.Context, _ uuid.UUID, h string) error {
	f.hash, f.prompted = h, true
	return errors.Join(f.err, f.fail("SetPIN"))
}
func (f *fakeStore) RemovePIN(context.Context, uuid.UUID) error { f.hash = ""; return f.err }
func (f *fakeStore) MarkPrompted(context.Context, uuid.UUID) error {
	f.prompted = true
	return f.err
}
func (f *fakeStore) LockSession(context.Context, uuid.UUID) (bool, error) {
	if e := errors.Join(f.err, f.fail("LockSession")); e != nil || !f.live || f.locked {
		return false, e
	}
	f.locked, f.failures = true, 0
	return true, nil
}
func (f *fakeStore) ClaimAttempt(context.Context, uuid.UUID) (int, bool, error) {
	if f.denyClaims > 0 {
		f.denyClaims--
		return 0, false, nil
	}
	if e := errors.Join(f.err, f.fail("ClaimAttempt")); e != nil || !f.live || !f.locked || f.failures >= screenlock.MaxUnlockFailures {
		return 0, false, e
	}
	f.failures++
	return f.failures, true, nil
}
func (f *fakeStore) UnlockSession(context.Context, uuid.UUID) error {
	f.locked, f.failures = false, 0
	return errors.Join(f.err, f.fail("UnlockSession"))
}
func (f *fakeStore) SessionState(context.Context, uuid.UUID) (screenlock.SessionState, bool, error) {
	return screenlock.SessionState{Locked: f.locked, Failures: f.failures}, f.live, errors.Join(f.err, f.fail("SessionState"))
}
func (f *fakeStore) RevokeSession(context.Context, uuid.UUID) error {
	f.live, f.revoked = false, true
	return errors.Join(f.err, f.fail("RevokeSession"))
}

var ctx = context.Background()

func lockedFake(t *testing.T, pin string) (*screenlock.Service, *fakeStore) {
	t.Helper()
	f := newFake()
	s := screenlock.NewService(f)
	if err := s.SetPIN(ctx, uuid.Nil, pin); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(ctx, uuid.Nil, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	return s, f
}

func TestSetPINRejectsNonDigitsAndLength(t *testing.T) {
	for _, tc := range []struct {
		pin string
		ok  bool
	}{
		{"123", false}, {"123456789", false}, {"12a4", false}, {" 1234", false}, {"", false},
		{"1234", true}, {"12345678", true},
	} {
		f := newFake()
		err := screenlock.NewService(f).SetPIN(ctx, uuid.Nil, tc.pin)
		if tc.ok {
			if err != nil {
				t.Fatalf("%q: %v", tc.pin, err)
			}
			if f.hash == tc.pin {
				t.Fatalf("%q stored in clear", tc.pin)
			}
			if m, verr := auth.VerifyPassword(f.hash, tc.pin); verr != nil || !m {
				t.Fatalf("%q does not verify: %v", tc.pin, verr)
			}
		} else if !errors.Is(err, screenlock.ErrInvalidPIN) || f.hash != "" {
			t.Fatalf("%q: err = %v, hash %q", tc.pin, err, f.hash)
		}
	}
}

func TestStatusAndPromptLifecycle(t *testing.T) {
	f := newFake()
	s := screenlock.NewService(f)
	check := func(want screenlock.Status) {
		t.Helper()
		got, err := s.Status(ctx, uuid.Nil)
		if err != nil || got != want {
			t.Fatalf("Status = %+v, %v; want %+v", got, err, want)
		}
	}
	check(screenlock.Status{Prompt: true})
	if err := s.SkipPrompt(ctx, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	check(screenlock.Status{}) // TestSkipPromptClearsPromptWithoutPIN
	if err := s.SetPIN(ctx, uuid.Nil, "1234"); err != nil {
		t.Fatal(err)
	}
	check(screenlock.Status{PINSet: true})
	if err := s.RemovePIN(ctx, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	check(screenlock.Status{}) // removing does not re-offer the prompt
}

func TestSetPINMarksPrompted(t *testing.T) {
	f := newFake()
	if err := screenlock.NewService(f).SetPIN(ctx, uuid.Nil, "1234"); err != nil || !f.prompted {
		t.Fatalf("prompted = %v, err %v", f.prompted, err)
	}
}

func TestLockWithoutPINIsErrNoPIN(t *testing.T) {
	f := newFake()
	if err := screenlock.NewService(f).Lock(ctx, uuid.Nil, uuid.Nil); !errors.Is(err, screenlock.ErrNoPIN) || f.locked {
		t.Fatalf("err = %v, locked %v", err, f.locked)
	}
}

func TestLockIdempotentAndMissingSession(t *testing.T) {
	s, f := lockedFake(t, "1234")
	if err := s.Lock(ctx, uuid.Nil, uuid.Nil); err != nil {
		t.Fatalf("second Lock: %v", err)
	}
	f.live = false
	if err := s.Lock(ctx, uuid.Nil, uuid.Nil); !errors.Is(err, screenlock.ErrNotFound) {
		t.Fatalf("Lock on dead session = %v", err)
	}
}

func TestUnlockRightPINClearsLockAndFailures(t *testing.T) {
	s, f := lockedFake(t, "1234")
	if _, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "0000"); !errors.Is(err, screenlock.ErrWrongPIN) {
		t.Fatal(err)
	}
	if _, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234"); err != nil {
		t.Fatal(err)
	}
	if f.locked || f.failures != 0 {
		t.Fatalf("locked %v failures %d", f.locked, f.failures)
	}
}

func TestUnlockWrongPINCountsDown(t *testing.T) {
	s, f := lockedFake(t, "1234")
	for want := 4; want >= 1; want-- {
		left, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "9999")
		if !errors.Is(err, screenlock.ErrWrongPIN) || left != want {
			t.Fatalf("left %d err %v, want %d", left, err, want)
		}
	}
	if _, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "9999"); !errors.Is(err, screenlock.ErrExhausted) || !f.revoked {
		t.Fatalf("err %v revoked %v", err, f.revoked)
	}
}

func TestUnlockAfterRightPINResetsCounter(t *testing.T) {
	s, _ := lockedFake(t, "1234")
	for range 3 {
		_, _ = s.Unlock(ctx, uuid.Nil, uuid.Nil, "9999")
	}
	if _, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(ctx, uuid.Nil, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	left, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "9999")
	if !errors.Is(err, screenlock.ErrWrongPIN) || left != 4 {
		t.Fatalf("left %d err %v", left, err)
	}
}

func TestUnlockMalformedPINSpendsAttempt(t *testing.T) {
	s, f := lockedFake(t, "1234")
	left, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "12 4")
	if !errors.Is(err, screenlock.ErrWrongPIN) || left != 4 || f.failures != 1 {
		t.Fatalf("left %d err %v failures %d", left, err, f.failures)
	}
}

func TestUnlockWithoutStoredPINCountsAsWrong(t *testing.T) {
	s, f := lockedFake(t, "1234")
	f.hash = ""
	if _, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234"); !errors.Is(err, screenlock.ErrWrongPIN) || !f.locked {
		t.Fatalf("err %v locked %v", err, f.locked)
	}
}

func TestUnlockNoClaimOutcomes(t *testing.T) {
	// not locked
	f := newFake()
	if _, err := screenlock.NewService(f).Unlock(ctx, uuid.Nil, uuid.Nil, "1234"); !errors.Is(err, screenlock.ErrNotLocked) {
		t.Fatalf("not locked: %v", err)
	}
	// gone
	f.live = false
	if _, err := screenlock.NewService(f).Unlock(ctx, uuid.Nil, uuid.Nil, "1234"); !errors.Is(err, screenlock.ErrNotFound) {
		t.Fatalf("gone: %v", err)
	}
	// exhausted by a racing request: failures already 5, still locked
	s, f2 := lockedFake(t, "1234")
	f2.failures = screenlock.MaxUnlockFailures
	if _, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234"); !errors.Is(err, screenlock.ErrExhausted) || !f2.revoked {
		t.Fatalf("exhausted: %v revoked %v", err, f2.revoked)
	}
}

func TestStoreErrorsAreWrapped(t *testing.T) {
	boom := errors.New("boom")
	f := newFake()
	f.err = boom
	s := screenlock.NewService(f)
	if _, err := s.Status(ctx, uuid.Nil); !errors.Is(err, boom) {
		t.Fatalf("Status: %v", err)
	}
	if err := s.SetPIN(ctx, uuid.Nil, "1234"); !errors.Is(err, boom) {
		t.Fatalf("SetPIN: %v", err)
	}
	if err := s.RemovePIN(ctx, uuid.Nil); !errors.Is(err, boom) {
		t.Fatalf("RemovePIN: %v", err)
	}
	if err := s.SkipPrompt(ctx, uuid.Nil); !errors.Is(err, boom) {
		t.Fatalf("SkipPrompt: %v", err)
	}
	if err := s.Lock(ctx, uuid.Nil, uuid.Nil); !errors.Is(err, boom) {
		t.Fatalf("Lock: %v", err)
	}
	if _, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234"); !errors.Is(err, boom) {
		t.Fatalf("Unlock: %v", err)
	}
}

func TestSingleStoreFailuresSurface(t *testing.T) {
	for _, tc := range []struct {
		method string
		run    func(*screenlock.Service, *fakeStore) error
	}{
		{"SetPIN", func(s *screenlock.Service, f *fakeStore) error { return s.SetPIN(ctx, uuid.Nil, "1234") }},
		{"LockSession", func(s *screenlock.Service, f *fakeStore) error { return s.Lock(ctx, uuid.Nil, uuid.Nil) }},
		{"SessionState", func(s *screenlock.Service, f *fakeStore) error {
			f.locked = true
			return s.Lock(ctx, uuid.Nil, uuid.Nil)
		}},
		{"ClaimAttempt", func(s *screenlock.Service, f *fakeStore) error {
			_, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234")
			return err
		}},
		{"PINState", func(s *screenlock.Service, f *fakeStore) error {
			_, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234")
			return err
		}},
		{"UnlockSession", func(s *screenlock.Service, f *fakeStore) error {
			_, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234")
			return err
		}},
		{"RevokeSession", func(s *screenlock.Service, f *fakeStore) error {
			f.failures = 4
			_, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "0000")
			return err
		}},
		{"SessionState", func(s *screenlock.Service, f *fakeStore) error {
			f.locked = false
			_, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234")
			return err
		}},
		{"RevokeSession", func(s *screenlock.Service, f *fakeStore) error {
			f.failures = 5
			_, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234")
			return err
		}},
	} {
		s, f := lockedFake(t, "1234")
		f.failOn = tc.method
		if tc.method == "SetPIN" || tc.method == "LockSession" {
			f.live, f.locked = true, false
		}
		if err := tc.run(s, f); !errors.Is(err, errBoom) {
			t.Fatalf("%s: err = %v", tc.method, err)
		}
	}
}

func TestSkipPromptClearsPromptWithoutPIN(t *testing.T) {
	f := newFake()
	s := screenlock.NewService(f)
	if err := s.SkipPrompt(ctx, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Status(ctx, uuid.Nil); err != nil || st.Prompt || st.PINSet {
		t.Fatalf("Status = %+v, %v", st, err)
	}
}

func TestRemovePINKeepsPromptedOff(t *testing.T) {
	f := newFake()
	s := screenlock.NewService(f)
	if err := s.SetPIN(ctx, uuid.Nil, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePIN(ctx, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Status(ctx, uuid.Nil); err != nil || st.Prompt || st.PINSet {
		t.Fatalf("Status = %+v, %v", st, err)
	}
}

func TestUnlockNotLocked(t *testing.T) {
	f := newFake()
	if _, err := screenlock.NewService(f).Unlock(ctx, uuid.Nil, uuid.Nil, "1234"); !errors.Is(err, screenlock.ErrNotLocked) {
		t.Fatalf("err = %v", err)
	}
}

// A late request misses its claim while the session is unlocked; the user
// locks again before it reads the session. The fresh lock must survive and
// the retried claim counts.
func TestUnlockNoClaimOnFreshLockRetriesWithoutRevoking(t *testing.T) {
	s, f := lockedFake(t, "1234")
	f.denyClaims = 1
	left, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "9999")
	if !errors.Is(err, screenlock.ErrWrongPIN) || left != 4 || f.revoked || f.failures != 1 {
		t.Fatalf("left %d err %v revoked %v failures %d", left, err, f.revoked, f.failures)
	}
	f.denyClaims = 1
	if _, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "1234"); err != nil || f.locked {
		t.Fatalf("retried right PIN: err %v locked %v", err, f.locked)
	}
}

func TestUnlockGivesUpAfterOneRetryWithoutRevoking(t *testing.T) {
	s, f := lockedFake(t, "1234")
	f.denyClaims = 2
	if _, err := s.Unlock(ctx, uuid.Nil, uuid.Nil, "9999"); !errors.Is(err, screenlock.ErrNotLocked) || f.revoked || !f.locked {
		t.Fatalf("err %v revoked %v locked %v", err, f.revoked, f.locked)
	}
}
