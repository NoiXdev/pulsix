package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

type fakeStore struct {
	users map[string]auth.User
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (auth.User, error) {
	u, ok := f.users[email]
	if !ok {
		return auth.User{}, auth.ErrNotFound
	}
	return u, nil
}
func (f *fakeStore) GetUserByID(_ context.Context, id uuid.UUID) (auth.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return auth.User{}, auth.ErrNotFound
}

type fakeSessions struct {
	created []uuid.UUID
	mfa     []bool
	revoked []uuid.UUID
}

func (f *fakeSessions) Create(_ context.Context, userID uuid.UUID, mfaComplete bool) (uuid.UUID, string, error) {
	f.created = append(f.created, userID)
	f.mfa = append(f.mfa, mfaComplete)
	return uuid.Must(uuid.NewV7()), "cookie", nil
}
func (f *fakeSessions) Revoke(_ context.Context, id uuid.UUID) error {
	f.revoked = append(f.revoked, id)
	return nil
}

type mfaOn struct{}

func (mfaOn) Enabled(context.Context, uuid.UUID) (bool, error) { return true, nil }

func TestLoginRejectsUnknownUserAndWrongPassword(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash}}}
	svc := auth.NewService(store, &fakeSessions{}, auth.NoMFA{})
	for _, tc := range []struct{ email, pw string }{{"nobody@x", "correct horse"}, {"a@x", "wrong"}} {
		_, err := svc.Login(context.Background(), tc.email, tc.pw)
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("%s/%s: err = %v", tc.email, tc.pw, err)
		}
	}
}

func TestLoginCreatesSessionAndFlagsMFA(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash}}}
	sess := &fakeSessions{}
	svc := auth.NewService(store, sess, mfaOn{})
	res, err := svc.Login(context.Background(), "a@x", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !res.MFARequired || res.Cookie != "cookie" || len(sess.created) != 1 || sess.mfa[0] {
		t.Fatalf("result %+v, sessions %+v", res, sess)
	}
}

func TestLoginSucceedsWithoutMFA(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash}}}
	sess := &fakeSessions{}
	svc := auth.NewService(store, sess, auth.NoMFA{})
	res, err := svc.Login(context.Background(), "a@x", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if res.MFARequired || res.Cookie != "cookie" || len(sess.created) != 1 || !sess.mfa[0] {
		t.Fatalf("result %+v, sessions %+v", res, sess)
	}
}

func TestServiceLogout(t *testing.T) {
	sess := &fakeSessions{}
	svc := auth.NewService(&fakeStore{}, sess, auth.NoMFA{})
	sid := uuid.Must(uuid.NewV7())
	if err := svc.Logout(context.Background(), auth.Principal{SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	if len(sess.revoked) != 1 || sess.revoked[0] != sid {
		t.Fatalf("revoked = %+v, want [%s]", sess.revoked, sid)
	}
}

func TestServiceMe(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", DisplayName: "A"}}}
	svc := auth.NewService(store, &fakeSessions{}, auth.NoMFA{})
	u, err := svc.Me(context.Background(), auth.Principal{UserID: uid})
	if err != nil || u.Email != "a@x" || u.DisplayName != "A" {
		t.Fatalf("Me = %+v, %v", u, err)
	}
	if _, err := svc.Me(context.Background(), auth.Principal{UserID: uuid.Must(uuid.NewV7())}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("Me(unknown) err = %v, want ErrNotFound", err)
	}
}
