package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// failingUserStore is a Store whose user lookup fails the way a database
// outage does, not the way a missing account does.
type failingUserStore struct {
	*fakeStore
	err error
}

func (f failingUserStore) GetUserByID(context.Context, uuid.UUID) (auth.User, error) {
	return auth.User{}, f.err
}

// VerifyOwnPassword is the renewal's password check (backlog 3.93): the
// session's own account, by id, under exactly Login's rules — a disabled
// account and an expired temporary password are refused with the same
// ErrInvalidCredentials a wrong password gets. It never opens a session.
func TestVerifyOwnPassword(t *testing.T) {
	hash, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	before, after := now.Add(-time.Second), now.Add(time.Second)
	uid := uuid.Must(uuid.NewV7())
	for _, tc := range []struct {
		name     string
		user     auth.User
		password string
		want     error
	}{
		{"right password", auth.User{ID: uid, PasswordHash: &hash}, "correct horse", nil},
		{"wrong password", auth.User{ID: uid, PasswordHash: &hash}, "wrong horse", auth.ErrInvalidCredentials},
		{"no password set", auth.User{ID: uid}, "correct horse", auth.ErrInvalidCredentials},
		{"disabled account", auth.User{ID: uid, PasswordHash: &hash, DisabledAt: &before}, "correct horse", auth.ErrInvalidCredentials},
		{"temporary password expired", auth.User{ID: uid, PasswordHash: &hash, TempPasswordExpiresAt: &before}, "correct horse", auth.ErrInvalidCredentials},
		{"temporary password expires exactly now", auth.User{ID: uid, PasswordHash: &hash, TempPasswordExpiresAt: &now}, "correct horse", auth.ErrInvalidCredentials},
		{"temporary password still valid", auth.User{ID: uid, PasswordHash: &hash, TempPasswordExpiresAt: &after}, "correct horse", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := tc.user
			u.Email = "a@x"
			sess := &fakeSessions{}
			svc := auth.NewService(&fakeStore{users: map[string]auth.User{"a@x": u}}, sess, mfaOn{}, func() time.Time { return now })
			if err := svc.VerifyOwnPassword(context.Background(), uid, tc.password); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("VerifyOwnPassword = %v, want %v", err, tc.want)
			}
			if len(sess.created) != 0 || len(sess.revoked) != 0 {
				t.Fatalf("sessions touched: created %v, revoked %v", sess.created, sess.revoked)
			}
		})
	}
}

func TestVerifyOwnPasswordUnknownAccount(t *testing.T) {
	svc := auth.NewService(&fakeStore{users: map[string]auth.User{}}, &fakeSessions{}, mfaOn{}, time.Now)
	if err := svc.VerifyOwnPassword(context.Background(), uuid.Must(uuid.NewV7()), "anything"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("VerifyOwnPassword(unknown account) = %v, want ErrInvalidCredentials", err)
	}
}

// A lookup that fails for any reason but a missing account is the store's
// error, not a wrong password: it must reach the caller as an internal
// failure (logged, 500), never as a refusal that charges the rate limit.
func TestVerifyOwnPasswordPropagatesStoreError(t *testing.T) {
	sentinel := errors.New("connection reset")
	svc := auth.NewService(failingUserStore{fakeStore: &fakeStore{}, err: sentinel}, &fakeSessions{}, mfaOn{}, time.Now)
	err := svc.VerifyOwnPassword(context.Background(), uuid.Must(uuid.NewV7()), "anything")
	if !errors.Is(err, sentinel) || errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("VerifyOwnPassword = %v, want the store's error", err)
	}
}
