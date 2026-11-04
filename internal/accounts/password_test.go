package accounts_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
)

// fakeStore is an in-memory accounts.Store. It records the writes the
// service makes, and InTx only reports whether fn ran inside it, so a test
// can tell that every write happened inside the transaction.
type fakeStore struct {
	hash      *string
	tempUntil *time.Time
	lookupErr error

	inTx          bool
	setCalls      []setPasswordCall
	revokeCalls   []revokeCall
	writesOutside int
}

type setPasswordCall struct {
	userID          uuid.UUID
	hash            string
	clearMustChange bool
}

type revokeCall struct{ userID, keep uuid.UUID }

func (f *fakeStore) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	f.inTx = true
	defer func() { f.inTx = false }()
	return fn(ctx)
}

func (f *fakeStore) LockPassword(_ context.Context, _ uuid.UUID) (accounts.PasswordState, error) {
	if f.lookupErr != nil {
		return accounts.PasswordState{}, f.lookupErr
	}
	return accounts.PasswordState{Hash: f.hash, TempPasswordExpiresAt: f.tempUntil}, nil
}

func (f *fakeStore) SetPassword(_ context.Context, userID uuid.UUID, hash string, clearMustChange bool) error {
	if !f.inTx {
		f.writesOutside++
	}
	f.setCalls = append(f.setCalls, setPasswordCall{userID, hash, clearMustChange})
	return nil
}

func (f *fakeStore) RevokeSessionsExcept(_ context.Context, userID, keep uuid.UUID) error {
	if !f.inTx {
		f.writesOutside++
	}
	f.revokeCalls = append(f.revokeCalls, revokeCall{userID, keep})
	return nil
}

// fakeMailer satisfies accounts.Mailer; ChangePassword sends nothing.
type fakeMailer struct{ sent int }

func (m *fakeMailer) Send(context.Context, string, string, string) error { m.sent++; return nil }
func (m *fakeMailer) Configured(context.Context) (bool, error)           { return true, nil }

func TestChangePassword(t *testing.T) {
	const current = "current password 1"
	oldHash, err := auth.HashPassword(current)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user, keep := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	past, future := now.Add(-time.Second), now.Add(time.Hour)

	for _, tc := range []struct {
		name      string
		hash      *string
		tempUntil *time.Time
		lookupErr error
		current   string
		next      string
		wantErr   error
	}{
		{name: "wrong current password", hash: &oldHash, current: "not the password", next: "a brand new password", wantErr: accounts.ErrInvalidCredentials},
		{name: "account without a password", current: current, next: "a brand new password", wantErr: accounts.ErrInvalidCredentials},
		{name: "unknown account", lookupErr: accounts.ErrNotFound, current: current, next: "a brand new password", wantErr: accounts.ErrNotFound},
		{name: "expired temporary password", hash: &oldHash, tempUntil: &past, current: current, next: "a brand new password", wantErr: accounts.ErrInvalidCredentials},
		{name: "eleven-character new password", hash: &oldHash, current: current, next: "elevenchars", wantErr: auth.ErrPasswordTooShort},
		{name: "new password equals the current one", hash: &oldHash, current: current, next: current, wantErr: accounts.ErrSamePassword},
		{name: "success", hash: &oldHash, current: current, next: "a brand new password"},
		{name: "success with a live temporary password", hash: &oldHash, tempUntil: &future, current: current, next: "a brand new password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{hash: tc.hash, tempUntil: tc.tempUntil, lookupErr: tc.lookupErr}
			mailer := &fakeMailer{}
			svc := accounts.NewService(store, mailer, "https://pulsix.example", func() time.Time { return now }, rand.Reader)

			err := svc.ChangePassword(context.Background(), user, keep, tc.current, tc.next)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ChangePassword err = %v, want %v", err, tc.wantErr)
			}
			if mailer.sent != 0 {
				t.Fatalf("ChangePassword sent %d mails, want none", mailer.sent)
			}
			if tc.wantErr != nil {
				if len(store.setCalls) != 0 || len(store.revokeCalls) != 0 {
					t.Fatalf("a refused change wrote: set %+v, revoke %+v", store.setCalls, store.revokeCalls)
				}
				return
			}
			if len(store.setCalls) != 1 {
				t.Fatalf("SetPassword calls = %+v, want exactly one", store.setCalls)
			}
			set := store.setCalls[0]
			if set.userID != user || !set.clearMustChange || set.hash == oldHash {
				t.Fatalf("SetPassword(%+v), want user %s, a new hash and clearMustChange", set, user)
			}
			if ok, err := auth.VerifyPassword(set.hash, tc.next); err != nil || !ok {
				t.Fatalf("the stored hash does not verify the new password: ok=%v err=%v", ok, err)
			}
			if len(store.revokeCalls) != 1 || store.revokeCalls[0] != (revokeCall{user, keep}) {
				t.Fatalf("RevokeSessionsExcept calls = %+v, want one for (%s, keep %s)", store.revokeCalls, user, keep)
			}
			if store.writesOutside != 0 {
				t.Fatalf("%d writes happened outside the transaction", store.writesOutside)
			}
		})
	}
}
