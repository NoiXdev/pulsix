package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// notFoundStore always reports the user as missing, exercising Login's
// account-enumeration-timing defense (Finding 2 fix round 1).
type notFoundStore struct{}

func (notFoundStore) GetUserByEmail(context.Context, string) (User, error) {
	return User{}, ErrNotFound
}

func (notFoundStore) GetUserByID(context.Context, uuid.UUID) (User, error) {
	return User{}, ErrNotFound
}

func (notFoundStore) SetLocale(context.Context, uuid.UUID, *string) error { return ErrNotFound }

// noopSessions never gets reached in this test (Login returns before it
// would create a session for an unknown user), but Service requires a
// SessionStore to construct.
type noopSessions struct{}

func (noopSessions) Create(context.Context, uuid.UUID, bool) (uuid.UUID, string, error) {
	return uuid.Nil, "", nil
}

func (noopSessions) Revoke(context.Context, uuid.UUID) error { return nil }

// TestLoginUnknownUserStillVerifiesDummyHash asserts that Login's timing
// side-channel fix is actually wired up: an unknown account must still
// invoke password verification against the fixed dummy hash before
// returning ErrInvalidCredentials. Asserting on wall-clock timing would be
// flaky, so this substitutes a spy for the package-level verifyPassword
// indirection and checks the observable call instead.
func TestLoginUnknownUserStillVerifiesDummyHash(t *testing.T) {
	orig := verifyPassword
	t.Cleanup(func() { verifyPassword = orig })

	var called bool
	var calledWith string
	verifyPassword = func(hash, pw string) (bool, error) {
		called = true
		calledWith = hash
		return orig(hash, pw)
	}

	svc := NewService(notFoundStore{}, noopSessions{}, NoMFA{})
	_, err := svc.Login(context.Background(), "nobody@example.test", "whatever")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	if !called {
		t.Fatal("verifyPassword was never invoked for an unknown user; the timing side channel is not closed")
	}
	if calledWith != dummyHash {
		t.Fatalf("verifyPassword called with hash %q, want the dummy hash", calledWith)
	}
}
