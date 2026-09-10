package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// checkerBool is a minimal MFAChecker fake with a fixed answer, for testing
// auth.AnyEnabled's own combining logic in isolation from any real checker
// (Finding 2, fix round 1).
type checkerBool struct {
	enabled bool
	err     error
	calls   int
}

func (c *checkerBool) Enabled(context.Context, uuid.UUID) (bool, error) {
	c.calls++
	return c.enabled, c.err
}

func TestAnyEnabledReportsTrueWhenAnyCheckerDoes(t *testing.T) {
	off := &checkerBool{enabled: false}
	on := &checkerBool{enabled: true}
	got, err := auth.AnyEnabled(off, on).Enabled(context.Background(), uuid.Must(uuid.NewV7()))
	if err != nil || !got {
		t.Fatalf("Enabled() = %v, %v, want true, nil", got, err)
	}
}

func TestAnyEnabledReportsFalseWhenNoCheckerDoes(t *testing.T) {
	a, b := &checkerBool{}, &checkerBool{}
	got, err := auth.AnyEnabled(a, b).Enabled(context.Background(), uuid.Must(uuid.NewV7()))
	if err != nil || got {
		t.Fatalf("Enabled() = %v, %v, want false, nil", got, err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls = %d, %d, want both checkers consulted when none report a factor", a.calls, b.calls)
	}
}

func TestAnyEnabledPropagatesFirstError(t *testing.T) {
	sentinel := errors.New("checker unavailable")
	failing := &checkerBool{err: sentinel}
	never := &checkerBool{enabled: true}
	_, err := auth.AnyEnabled(failing, never).Enabled(context.Background(), uuid.Must(uuid.NewV7()))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want to wrap %v", err, sentinel)
	}
	if never.calls != 0 {
		t.Fatal("a later checker was consulted despite an earlier one failing — AnyEnabled must fail closed immediately, not mask the error with a later true")
	}
}

// TestPasskeysSatisfiesMFAChecker is a compile-time assertion that
// *auth.Passkeys implements auth.MFAChecker, the seam Finding 2 (fix round
// 1) relies on to construct the login service with
// auth.AnyEnabled(totp, passkeys).
var _ auth.MFAChecker = (*auth.Passkeys)(nil)

func TestPasskeysEnabledReflectsWhetherAnyCredentialExists(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newMemPasskeyStore(clock)
	pk, uid := newPasskeys(t, store, clock)

	got, err := pk.Enabled(context.Background(), uid)
	if err != nil || got {
		t.Fatalf("Enabled() = %v, %v, want false, nil for a user with no passkeys", got, err)
	}

	store.listResult = []auth.Credential{{ID: uuid.Must(uuid.NewV7())}}
	got, err = pk.Enabled(context.Background(), uid)
	if err != nil || !got {
		t.Fatalf("Enabled() = %v, %v, want true, nil once the user has a passkey", got, err)
	}

	store.listErr = errors.New("store unavailable")
	if _, err := pk.Enabled(context.Background(), uid); err == nil {
		t.Fatal("expected ListCredentials' error to propagate, not be swallowed into false")
	}
}

// TestLoginWithAnyEnabledChecksBothFactors asserts Finding 2's fix at the
// Service.Login level, the three scenarios the fix round specifies: a user
// with only a passkey gets a pending session from a password login; a user
// with neither factor gets a complete one; an error from either checker
// prevents an MFA-complete session from ever being created. Before this
// fix, mfaRequired came from a single MFAChecker (TOTP alone), so
// registering a passkey never changed it — a stolen password alone
// produced an MFA-complete session for a passkey-only account, which then
// satisfied requireMFAComplete on every passkey management route.
func TestLoginWithAnyEnabledChecksBothFactors(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash}}}

	t.Run("passkey only is still a second factor", func(t *testing.T) {
		sess := &fakeSessions{}
		svc := auth.NewService(store, sess, auth.AnyEnabled(auth.NoMFA{}, mfaOn{}))
		res, err := svc.Login(context.Background(), "a@x", "correct horse")
		if err != nil {
			t.Fatal(err)
		}
		if !res.MFARequired || len(sess.mfa) != 1 || sess.mfa[0] {
			t.Fatalf("a passkey-only account must get a pending (mfa_complete=false) session, got %+v, sessions %+v", res, sess)
		}
	})

	t.Run("neither factor enrolled", func(t *testing.T) {
		sess := &fakeSessions{}
		svc := auth.NewService(store, sess, auth.AnyEnabled(auth.NoMFA{}, auth.NoMFA{}))
		res, err := svc.Login(context.Background(), "a@x", "correct horse")
		if err != nil {
			t.Fatal(err)
		}
		if res.MFARequired || len(sess.mfa) != 1 || !sess.mfa[0] {
			t.Fatalf("an account with no second factor must get a complete session, got %+v, sessions %+v", res, sess)
		}
	})

	t.Run("either checker erroring prevents an MFA-complete session", func(t *testing.T) {
		sentinel := errors.New("checker down")
		for _, mfa := range []auth.MFAChecker{
			auth.AnyEnabled(mfaErr{err: sentinel}, auth.NoMFA{}),
			auth.AnyEnabled(auth.NoMFA{}, mfaErr{err: sentinel}),
		} {
			sess := &fakeSessions{}
			svc := auth.NewService(store, sess, mfa)
			if _, err := svc.Login(context.Background(), "a@x", "correct horse"); !errors.Is(err, sentinel) {
				t.Fatalf("Login err = %v, want to wrap %v", err, sentinel)
			}
			if len(sess.created) != 0 {
				t.Fatal("a session was created despite the MFA check failing")
			}
		}
	})
}
