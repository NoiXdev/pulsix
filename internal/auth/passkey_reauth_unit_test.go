package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// lockingSessions is an auth.LoginSessions whose account lock fails with
// err, standing in for an account that vanished (ErrNotFound) or a store
// outage between the assertion's check and the counter update.
type lockingSessions struct {
	fakeSessions
	err error
}

func (l *lockingSessions) LockAccount(context.Context, uuid.UUID) error { return l.err }

// reauthUnit is a re-authentication against fakes: one account with one
// passkey whose assertions the test signs.
type reauthUnit struct {
	pk    *auth.Passkeys
	store *memPasskeyStore
	users *fakeStore
	uid   uuid.UUID
	key   loginKey
}

func newReauthUnit(t *testing.T, sessions auth.LoginSessions) reauthUnit {
	t.Helper()
	store := newMemPasskeyStore(time.Now)
	uid := uuid.Must(uuid.NewV7())
	users := &fakeStore{users: map[string]auth.User{"p@x": {ID: uid, Email: "p@x", DisplayName: "P"}}}
	pk, err := auth.NewPasskeys(store, users, sessions, inlineTx{}, testRPID, "Pulsix Test", testOrigin, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k := loginKey{credID: []byte("reauth-unit-credential"), priv: priv}
	store.listResult = []auth.Credential{{CredentialID: k.credID, PublicKey: buildCOSEPublicKey(t, &priv.PublicKey)}}
	store.findUserID = &uid
	return reauthUnit{pk: pk, store: store, users: users, uid: uid, key: k}
}

func (r reauthUnit) finish(t *testing.T) error {
	t.Helper()
	cid, response := beginReauth(t, r.pk, r.uid, r.uid, r.key, 1)
	return r.pk.FinishReauth(context.Background(), r.uid, cid, response)
}

func TestBeginReauthPropagatesStoreErrors(t *testing.T) {
	sentinel := errors.New("store down")
	t.Run("unknown account", func(t *testing.T) {
		r := newReauthUnit(t, &fakeSessions{})
		if _, _, err := r.pk.BeginReauth(context.Background(), uuid.Must(uuid.NewV7())); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("BeginReauth = %v, want ErrNotFound", err)
		}
	})
	t.Run("credential list", func(t *testing.T) {
		r := newReauthUnit(t, &fakeSessions{})
		r.store.listErr = sentinel
		if _, _, err := r.pk.BeginReauth(context.Background(), r.uid); !errors.Is(err, sentinel) {
			t.Fatalf("BeginReauth = %v, want %v", err, sentinel)
		}
	})
	t.Run("ceremony store", func(t *testing.T) {
		r := newReauthUnit(t, &fakeSessions{})
		r.store.putErr = sentinel
		if _, _, err := r.pk.BeginReauth(context.Background(), r.uid); !errors.Is(err, sentinel) {
			t.Fatalf("BeginReauth = %v, want %v", err, sentinel)
		}
	})
}

func TestFinishReauthAdvancesTheCounter(t *testing.T) {
	r := newReauthUnit(t, &fakeSessions{})
	if err := r.finish(t); err != nil {
		t.Fatalf("FinishReauth: %v", err)
	}
	if len(r.store.updateCounterCalls) != 1 || r.store.updateCounterCalls[0].counter != 1 {
		t.Fatalf("counter updates = %+v, want one to 1", r.store.updateCounterCalls)
	}
}

// The credential is re-read under the account lock (claimAssertion): one
// that moved to another account, or vanished, between the signature check
// and the lock is refused like an unregistered one, and its counter stays.
func TestFinishReauthRechecksTheCredentialUnderTheLock(t *testing.T) {
	other := uuid.Must(uuid.NewV7())
	for _, tc := range []struct {
		name  string
		setup func(r reauthUnit)
	}{
		{"owned by another account", func(r reauthUnit) { r.store.findUserID = &other }},
		{"removed", func(r reauthUnit) { r.store.findUserID = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReauthUnit(t, &fakeSessions{})
			cid, response := beginReauth(t, r.pk, r.uid, r.uid, r.key, 1)
			tc.setup(r)
			if err := r.pk.FinishReauth(context.Background(), r.uid, cid, response); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("FinishReauth = %v, want ErrInvalidCredentials", err)
			}
			if len(r.store.updateCounterCalls) != 0 {
				t.Fatalf("counter updated: %+v", r.store.updateCounterCalls)
			}
		})
	}
}

// A failure of the store is the manager's, not a wrong assertion: it is
// returned as itself, never folded into ErrInvalidCredentials.
func TestFinishReauthPropagatesStoreErrors(t *testing.T) {
	sentinel := errors.New("store down")
	t.Run("credential lookup", func(t *testing.T) {
		r := newReauthUnit(t, &fakeSessions{})
		cid, response := beginReauth(t, r.pk, r.uid, r.uid, r.key, 1)
		r.store.findErr = sentinel
		if err := r.pk.FinishReauth(context.Background(), r.uid, cid, response); !errors.Is(err, sentinel) {
			t.Fatalf("FinishReauth = %v, want %v", err, sentinel)
		}
	})
	t.Run("account lock", func(t *testing.T) {
		r := newReauthUnit(t, &lockingSessions{err: sentinel})
		if err := r.finish(t); !errors.Is(err, sentinel) {
			t.Fatalf("FinishReauth = %v, want %v", err, sentinel)
		}
	})
	t.Run("account gone at the lock", func(t *testing.T) {
		r := newReauthUnit(t, &lockingSessions{err: auth.ErrNotFound})
		if err := r.finish(t); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("FinishReauth = %v, want ErrInvalidCredentials", err)
		}
	})
	t.Run("account gone after the ceremony began", func(t *testing.T) {
		r := newReauthUnit(t, &fakeSessions{})
		cid, response := beginReauth(t, r.pk, r.uid, r.uid, r.key, 1)
		delete(r.users.users, "p@x")
		if err := r.pk.FinishReauth(context.Background(), r.uid, cid, response); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("FinishReauth = %v, want ErrNotFound", err)
		}
	})
}
