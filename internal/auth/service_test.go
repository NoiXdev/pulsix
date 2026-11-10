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
func (f *fakeStore) SetLocale(_ context.Context, id uuid.UUID, l *string) error {
	for email, u := range f.users {
		if u.ID == id {
			u.Locale = l
			f.users[email] = u
			return nil
		}
	}
	return auth.ErrNotFound
}

type fakeSessions struct {
	created []uuid.UUID
	mfa     []bool
	revoked []uuid.UUID
}

// LockAccount makes fakeSessions an auth.LoginSessions; the fakes hold no
// rows, so there is nothing to lock.
func (f *fakeSessions) LockAccount(context.Context, uuid.UUID) error { return nil }

func (f *fakeSessions) Create(_ context.Context, userID uuid.UUID, mfaComplete bool) (uuid.UUID, string, error) {
	f.created = append(f.created, userID)
	f.mfa = append(f.mfa, mfaComplete)
	return uuid.Must(uuid.NewV7()), "cookie", nil
}
func (f *fakeSessions) Revoke(_ context.Context, id uuid.UUID) error {
	f.revoked = append(f.revoked, id)
	return nil
}

// inlineTx is the TxRunner of the unit tests: the fakes have no
// transaction, so fn just runs.
type inlineTx struct{}

func (inlineTx) WithInstanceTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
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
	svc := auth.NewService(store, sess, mfaErr{err: sentinel}, time.Now)

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
	svc := auth.NewService(store, sess, auth.NoMFA{}, time.Now)
	if _, err := svc.Login(context.Background(), "a@x", "correct horse"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("Login(disabled user, correct password) err = %v, want ErrInvalidCredentials", err)
	}
	if len(sess.created) != 0 {
		t.Fatalf("a session was created for a disabled user: %+v", sess.created)
	}
}

// TestLoginRefusesExpiredTemporaryPassword: a temporary password an
// administrator set stops working once temp_password_expires_at has
// passed. Like a disabled account it answers ErrInvalidCredentials, after
// the argon2id verification, so the endpoint reveals nothing about which
// accounts carry an expired temporary password. Before the expiry it
// still signs in.
func TestLoginRefusesExpiredTemporaryPassword(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	for _, tc := range []struct {
		name    string
		expires time.Time
		wantErr error
	}{
		{"expired a second ago", now.Add(-time.Second), auth.ErrInvalidCredentials},
		{"expires exactly now", now, auth.ErrInvalidCredentials},
		{"still valid", now.Add(time.Second), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exp := tc.expires
			store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash, TempPasswordExpiresAt: &exp}}}
			sess := &fakeSessions{}
			svc := auth.NewService(store, sess, auth.NoMFA{}, clock)
			_, err := svc.Login(context.Background(), "a@x", "correct horse")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Login err = %v, want %v", err, tc.wantErr)
			}
			wantSessions := 0
			if tc.wantErr == nil {
				wantSessions = 1
			}
			if len(sess.created) != wantSessions {
				t.Fatalf("sessions created = %d, want %d", len(sess.created), wantSessions)
			}
		})
	}
}

func TestLoginRejectsUnknownUserAndWrongPassword(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash}}}
	svc := auth.NewService(store, &fakeSessions{}, auth.NoMFA{}, time.Now)
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
	svc := auth.NewService(store, sess, mfaOn{}, time.Now)
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
	svc := auth.NewService(store, sess, auth.NoMFA{}, time.Now)
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
	svc := auth.NewService(&fakeStore{}, sess, auth.NoMFA{}, time.Now)
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
	svc := auth.NewService(store, &fakeSessions{}, auth.NoMFA{}, time.Now)
	u, err := svc.Me(context.Background(), auth.Principal{UserID: uid})
	if err != nil || u.Email != "a@x" || u.DisplayName != "A" {
		t.Fatalf("Me = %+v, %v", u, err)
	}
	if _, err := svc.Me(context.Background(), auth.Principal{UserID: uuid.Must(uuid.NewV7())}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("Me(unknown) err = %v, want ErrNotFound", err)
	}
}

func strPtr(s string) *string { return &s }

// TestSetLocale: a user's own GUI language is stored, replaced and cleared,
// and only the shipped languages are accepted (backlog 1.41).
func TestSetLocale(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x"}}}
	svc := auth.NewService(store, &fakeSessions{}, auth.NoMFA{}, time.Now)
	ctx := context.Background()
	p := auth.Principal{UserID: uid}

	me := func() auth.User {
		t.Helper()
		u, err := svc.Me(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	if me().Locale != nil {
		t.Fatalf("a fresh user has locale %q, want none", *me().Locale)
	}
	if err := svc.SetLocale(ctx, uid, strPtr("de")); err != nil {
		t.Fatal(err)
	}
	if l := me().Locale; l == nil || *l != "de" {
		t.Fatalf("locale after SetLocale(de) = %v, want de", l)
	}
	if err := svc.SetLocale(ctx, uid, nil); err != nil {
		t.Fatal(err)
	}
	if me().Locale != nil {
		t.Fatal("SetLocale(nil) did not clear the language")
	}
	if err := svc.SetLocale(ctx, uid, strPtr("de")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"fr", "", "DE", "de-DE"} {
		if err := svc.SetLocale(ctx, uid, strPtr(bad)); !errors.Is(err, auth.ErrInvalidInput) {
			t.Errorf("SetLocale(%q) err = %v, want ErrInvalidInput", bad, err)
		}
	}
	if l := me().Locale; l == nil || *l != "de" {
		t.Fatalf("a refused SetLocale changed the language to %v", l)
	}
}
