package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// TestLoginOptionalSecondFactor covers PULSIX_MFA_OPTIONAL at the service
// level: with the mode off nothing changes (an account without a factor
// gets a pending session that must enrol one); with it on such an account
// gets a complete session from its password alone; an account with a TOTP
// secret or a passkey still has to verify it in both modes.
func TestLoginOptionalSecondFactor(t *testing.T) {
	hash, _ := auth.HashPassword("correct horse")
	uid := uuid.Must(uuid.NewV7())
	store := &fakeStore{users: map[string]auth.User{"a@x": {ID: uid, Email: "a@x", PasswordHash: &hash}}}

	cases := []struct {
		name         string
		optional     bool
		mfa          auth.MFAChecker
		wantComplete bool
		wantRequired bool
		wantEnrol    bool
	}{
		{name: "mode off, no factor", optional: false, mfa: auth.AnyEnabled(auth.NoMFA{}, auth.NoMFA{}), wantEnrol: true},
		{name: "mode on, no factor", optional: true, mfa: auth.AnyEnabled(auth.NoMFA{}, auth.NoMFA{}), wantComplete: true},
		{name: "mode off, TOTP", optional: false, mfa: auth.AnyEnabled(mfaOn{}, auth.NoMFA{}), wantRequired: true},
		{name: "mode on, TOTP", optional: true, mfa: auth.AnyEnabled(mfaOn{}, auth.NoMFA{}), wantRequired: true},
		{name: "mode off, passkey", optional: false, mfa: auth.AnyEnabled(auth.NoMFA{}, mfaOn{}), wantRequired: true},
		{name: "mode on, passkey", optional: true, mfa: auth.AnyEnabled(auth.NoMFA{}, mfaOn{}), wantRequired: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := &fakeSessions{}
			svc := auth.NewService(store, sess, tc.mfa, time.Now, auth.WithOptionalSecondFactor(tc.optional))
			res, err := svc.Login(context.Background(), "a@x", "correct horse")
			if err != nil {
				t.Fatal(err)
			}
			if len(sess.mfa) != 1 || sess.mfa[0] != tc.wantComplete {
				t.Fatalf("session complete = %v, want %v", sess.mfa, tc.wantComplete)
			}
			if res.MFARequired != tc.wantRequired || res.EnrolmentRequired != tc.wantEnrol {
				t.Fatalf("result = %+v, want MFARequired %v, EnrolmentRequired %v", res, tc.wantRequired, tc.wantEnrol)
			}
			if res.WithoutSecondFactor != tc.wantComplete {
				t.Fatalf("WithoutSecondFactor = %v, want %v", res.WithoutSecondFactor, tc.wantComplete)
			}
			if res.UserID != uid || res.SessionID == uuid.Nil {
				t.Fatalf("result names user %s session %s, want %s and a session", res.UserID, res.SessionID, uid)
			}
		})
	}
}
