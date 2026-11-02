package auth

// This file is package auth (not auth_test), same pattern as
// service_dummy_hash_test.go's spy on verifyPassword: it exercises the
// userHandleMismatch indirection FinishLogin's DiscoverableUserHandler uses
// (Finding 4, fix round 1).
//
// TestFinishLoginRejectsUserHandleMismatch (passkey_test.go, package
// auth_test) drives the full public FinishLogin path and still passes with
// this codebase's own check removed, because go-webauthn's own
// validateLogin independently re-checks the identical property once the
// handler returns — through the public API the two are indistinguishable,
// both collapsing into the same generic ErrInvalidCredentials. The two
// tests below instead isolate this codebase's own check: one proves its
// comparison logic is correct, the other proves FinishLogin's handler
// actually calls it on every login attempt (so deleting the call site, not
// just its logic, would also be caught).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestUserHandleMismatchDetectsForgedHandle proves userHandleMismatch's own
// comparison logic: a userHandle equal to the credential's resolved owner
// is not a mismatch, and any other userHandle is.
func TestUserHandleMismatchDetectsForgedHandle(t *testing.T) {
	owner := uuid.Must(uuid.NewV7())
	if userHandleMismatch(owner[:], owner) {
		t.Fatal("a userHandle equal to the owner's id was reported as a mismatch")
	}
	forged := uuid.Must(uuid.NewV7())
	if !userHandleMismatch(forged[:], owner) {
		t.Fatal("a forged userHandle was not detected as a mismatch")
	}
}

// tinyPasskeyStore is a minimal in-package PasskeyStore fake for
// TestLocalUserHandleCheckReallyRuns: package auth's test files cannot use
// auth_test's memPasskeyStore (a different test package), so this
// duplicates just enough of it, following the same precedent as
// service_dummy_hash_test.go's notFoundStore/noopSessions.
type tinyPasskeyStore struct {
	ceremonies map[uuid.UUID]tinyCeremony
	findUserID uuid.UUID
}

type tinyCeremony struct {
	kind string
	user *uuid.UUID
	data []byte
}

func (s *tinyPasskeyStore) ListCredentials(context.Context, uuid.UUID) ([]Credential, error) {
	return nil, nil
}
func (s *tinyPasskeyStore) AddCredential(context.Context, uuid.UUID, string, Credential) error {
	return nil
}
func (s *tinyPasskeyStore) UpdateCredentialCounter(context.Context, []byte, uint32) error { return nil }
func (s *tinyPasskeyStore) DeleteCredential(context.Context, uuid.UUID, uuid.UUID) error  { return nil }
func (s *tinyPasskeyStore) FindUserByCredentialID(context.Context, []byte) (uuid.UUID, error) {
	return s.findUserID, nil
}
func (s *tinyPasskeyStore) PutCeremony(_ context.Context, id uuid.UUID, kind string, user *uuid.UUID, data []byte, _ time.Time) error {
	s.ceremonies[id] = tinyCeremony{kind: kind, user: user, data: data}
	return nil
}
func (s *tinyPasskeyStore) TakeCeremony(_ context.Context, id uuid.UUID) (string, *uuid.UUID, []byte, error) {
	c, ok := s.ceremonies[id]
	if !ok {
		return "", nil, nil, ErrNotFound
	}
	delete(s.ceremonies, id)
	return c.kind, c.user, c.data, nil
}

type tinyUserStore struct{ u User }

func (s tinyUserStore) GetUserByEmail(context.Context, string) (User, error) { return s.u, nil }
func (s tinyUserStore) GetUserByID(context.Context, uuid.UUID) (User, error) { return s.u, nil }

func (s tinyUserStore) SetLocale(context.Context, uuid.UUID, *string) error { return nil }

type tinySessions struct{}

func (tinySessions) Create(context.Context, uuid.UUID, bool) (uuid.UUID, string, error) {
	return uuid.Nil, "", nil
}
func (tinySessions) Revoke(context.Context, uuid.UUID) error { return nil }

// TestLocalUserHandleCheckReallyRuns proves FinishLogin's handler actually
// invokes userHandleMismatch on every login attempt that reaches it, by
// substituting a spy for the package-level indirection (the same technique
// TestLoginUnknownUserStillVerifiesDummyHash uses for verifyPassword) and
// driving a full FinishLogin call with a forged user handle. Together with
// TestUserHandleMismatchDetectsForgedHandle, this covers what
// TestFinishLoginRejectsUserHandleMismatch's black-box assertion no longer
// can on its own: that this codebase's own check exists, runs, and works —
// not only that go-webauthn's independent check does.
func TestLocalUserHandleCheckReallyRuns(t *testing.T) {
	orig := userHandleMismatch
	t.Cleanup(func() { userHandleMismatch = orig })
	var called bool
	userHandleMismatch = func(userHandle []byte, owner uuid.UUID) bool {
		called = true
		return orig(userHandle, owner)
	}

	owner := uuid.Must(uuid.NewV7())
	store := &tinyPasskeyStore{ceremonies: map[uuid.UUID]tinyCeremony{}, findUserID: owner}
	users := tinyUserStore{u: User{ID: owner, Email: "o@x", DisplayName: "O"}}
	pk, err := NewPasskeys(store, users, tinySessions{}, "localhost", "Pulsix Test", "http://localhost:5173", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, cid, err := pk.BeginLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// A structurally valid (but unsigned) assertion is enough to reach the
	// handler closure — signature verification happens after the handler
	// returns, and this mismatch is rejected by the handler itself before
	// that point, same technique as auth_test's makeAssertionResponse.
	authData := make([]byte, 37)
	forgedHandle := uuid.Must(uuid.NewV7())
	credID := []byte("cred")
	body := map[string]any{
		"id": base64.RawURLEncoding.EncodeToString(credID), "rawId": base64.RawURLEncoding.EncodeToString(credID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString([]byte(`{}`)),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString([]byte("sig")),
			"userHandle":        base64.RawURLEncoding.EncodeToString(forgedHandle[:]),
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pk.FinishLogin(context.Background(), cid, b); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("FinishLogin err = %v, want ErrInvalidCredentials", err)
	}
	if !called {
		t.Fatal("userHandleMismatch was never invoked; FinishLogin's local user-handle check is not wired up")
	}
}
