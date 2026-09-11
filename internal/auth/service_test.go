package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// mfaErr simulates an MFAChecker.Enabled that fails, e.g. auth.TOTP.Enabled
// propagating a database error rather than swallowing it (Finding 1, fix
// round 1). The seam Finding 4 introduced (MFAChecker as an interface, not
// a concrete *auth.TOTP) is what makes this fake possible at all.
type mfaErr struct{ err error }

func (m mfaErr) Enabled(context.Context, uuid.UUID) (bool, error) { return false, m.err }

// TestLoginFailsClosedOnMFACheckError asserts Finding 1's consequence at
// the Service.Login level: if the MFAChecker cannot even report whether a
// factor is enrolled, Login must fail rather than fall back to
// mfaRequired == false and create an MFA-complete session. Before the fix,
// auth.TOTP.Enabled swallowed every store error into (false, nil), which
// made this branch unreachable — a statement timeout or a connection
// reset silently downgraded a TOTP-protected account to password-only
// login.
func TestLoginFailsClosedOnMFACheckError(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash}}}
	sess := &fakeSessions{}
	sentinel := errors.New("mfa store unavailable")
	svc := auth.NewService(store, sess, mfaErr{err: sentinel})

	if _, err := svc.Login(context.Background(), "a@x", "correct horse"); !errors.Is(err, sentinel) {
		t.Fatalf("Login err = %v, want to wrap %v", err, sentinel)
	}
	if len(sess.created) != 0 {
		t.Fatalf("a session was created despite the MFA check failing: %+v", sess.created)
	}
}

// TestLoginRejectsDisabledUser is Finding 2's password-login regression
// test (Task 10 fix round 1): a disabled account with an otherwise-correct
// password must not be able to log back in and mint a fresh session — the
// exact gap the review found (DeactivateUser revoked sessions and tokens,
// but nothing read disabled_at, so the same password just opened a new
// one). The response is the same ErrInvalidCredentials a wrong password
// produces, not a distinct error, so login cannot be used to probe which
// accounts are disabled.
func TestLoginRejectsDisabledUser(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	now := time.Now()
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash, DisabledAt: &now}}}
	sess := &fakeSessions{}
	svc := auth.NewService(store, sess, auth.NoMFA{})
	if _, err := svc.Login(context.Background(), "a@x", "correct horse"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("Login(disabled user, correct password) err = %v, want ErrInvalidCredentials", err)
	}
	if len(sess.created) != 0 {
		t.Fatalf("a session was created for a disabled user: %+v", sess.created)
	}
}

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
	if !res.MFARequired || res.EnrolmentRequired || res.Cookie != "cookie" || len(sess.created) != 1 || sess.mfa[0] {
		t.Fatalf("result %+v, sessions %+v", res, sess)
	}
}

// TestLoginWithoutAFactorRequiresEnrolment: a second factor is mandatory,
// so a password login to an account that has none opens a session that is
// not MFA-complete and flags enrolment instead of verification.
func TestLoginWithoutAFactorRequiresEnrolment(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash}}}
	sess := &fakeSessions{}
	svc := auth.NewService(store, sess, auth.NoMFA{})
	res, err := svc.Login(context.Background(), "a@x", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if res.MFARequired || !res.EnrolmentRequired || res.Cookie != "cookie" || len(sess.created) != 1 || sess.mfa[0] {
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
