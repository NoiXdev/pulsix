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
		{name: "expired temporary password", hash: &oldHash, tempUntil: &past, current: current, next: "a brand new password", wantErr: accounts.ErrTemporaryPasswordExpired},
		{name: "wrong password while the temporary one has expired", hash: &oldHash, tempUntil: &past, current: "not the password", next: "a brand new password", wantErr: accounts.ErrInvalidCredentials},
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
			if len(mailer.sent) != 0 {
				t.Fatalf("ChangePassword sent %d mails, want none", len(mailer.sent))
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
