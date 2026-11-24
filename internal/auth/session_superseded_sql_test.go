// A password-only session (PULSIX_MFA_OPTIONAL) that a second factor has
// superseded is still mfa_complete in its row but resolves to a pending
// principal. The admin session count must not include it, and proving the
// factor on it is a completed sign-in that stamps users.last_sign_in_at
// (backlog 3.113). These tests drive the real database.
package auth_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func (w *resetWorld) activeSessions(t *testing.T, uid uuid.UUID) int64 {
	t.Helper()
	row, err := w.q.GetAccount(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	return row.ActiveSessions
}

func (w *resetWorld) setLastSignIn(t *testing.T, uid uuid.UUID, at any) {
	t.Helper()
	if _, err := w.d.Pool().Exec(ctx, `UPDATE users SET last_sign_in_at = $2 WHERE id = $1`, uid, at); err != nil {
		t.Fatal(err)
	}
}

func (w *resetWorld) lastSignIn(t *testing.T, uid uuid.UUID) *time.Time {
	t.Helper()
	var at *time.Time
	if err := w.d.Pool().QueryRow(ctx, `SELECT last_sign_in_at FROM users WHERE id = $1`, uid).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestActiveSessionsCountsOnlyUsableSessions(t *testing.T) {
	for _, tc := range []struct {
		name         string
		passwordOnly bool // the mode is on: the session rests on the password alone
		factor       string
		want         int64
	}{
		{name: "mode off, factor", factor: "totp", want: 1},
		{name: "mode off, no factor", want: 1},
		{name: "mode on, no factor", passwordOnly: true, want: 1},
		{name: "mode on, unconfirmed totp is no factor", passwordOnly: true, factor: "pending totp", want: 1},
		{name: "mode on, superseded by totp", passwordOnly: true, factor: "totp", want: 0},
		{name: "mode on, superseded by a passkey", passwordOnly: true, factor: "passkey", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "count@example.test")
			var err error
			if tc.passwordOnly {
				_, _, err = w.sessions.CreatePasswordOnly(ctx, uid)
			} else {
				_, _, err = w.sessions.Create(ctx, uid, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.factor != "" {
				w.addFactor(t, uid, tc.factor)
			}
			if got := w.activeSessions(t, uid); got != tc.want {
				t.Fatalf("active_sessions = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestActiveSessionsCountsASupersededSessionOnceItProvedTheFactor(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "reverify@example.test")
	id, _, err := w.sessions.CreatePasswordOnly(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	w.addFactor(t, uid, "totp")
	if got := w.activeSessions(t, uid); got != 0 {
		t.Fatalf("superseded: active_sessions = %d, want 0", got)
	}
	if _, err := w.sessions.MarkMFAComplete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := w.activeSessions(t, uid); got != 1 {
		t.Fatalf("after the factor: active_sessions = %d, want 1 (the re-verified row, not a second one)", got)
	}
}

func TestMarkMFACompleteStampsTheSignInOfASupersededSession(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name      string
		open      func(w *resetWorld, uid uuid.UUID) (uuid.UUID, error)
		factor    string
		wantStamp bool
	}{
		{name: "superseded by totp", factor: "totp", wantStamp: true, open: openPasswordOnly},
		{name: "superseded by a passkey", factor: "passkey", wantStamp: true, open: openPasswordOnly},
		{name: "password-only, still no factor: a step-up is no sign-in", open: openPasswordOnly},
		{name: "mode off, step-up of a complete session", factor: "totp", open: func(w *resetWorld, uid uuid.UUID) (uuid.UUID, error) {
			id, _, err := w.sessions.Create(ctx, uid, true)
			return id, err
		}},
		{name: "pending session", factor: "totp", wantStamp: true, open: func(w *resetWorld, uid uuid.UUID) (uuid.UUID, error) {
			id, _, err := w.sessions.Create(ctx, uid, false)
			return id, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "stamp@example.test")
			id, err := tc.open(w, uid)
			if err != nil {
				t.Fatal(err)
			}
			if tc.factor != "" {
				w.addFactor(t, uid, tc.factor)
			}
			w.setLastSignIn(t, uid, old)
			if _, err := w.sessions.MarkMFAComplete(ctx, id); err != nil {
				t.Fatal(err)
			}
			got := w.lastSignIn(t, uid)
			if got == nil {
				t.Fatal("last_sign_in_at is NULL")
			}
			if stamped := got.After(old.Add(time.Hour)); stamped != tc.wantStamp {
				t.Fatalf("last_sign_in_at = %v (stamped %v), want stamped %v", got, stamped, tc.wantStamp)
			}
		})
	}
}

func openPasswordOnly(w *resetWorld, uid uuid.UUID) (uuid.UUID, error) {
	id, _, err := w.sessions.CreatePasswordOnly(ctx, uid)
	return id, err
}

// Enrolling an account's first factor from a password-only session marks the
// session complete but is no sign-in: the password sign-in already stamped
// last_sign_in_at when the session was opened (backlog 3.113).
func TestConfirmTOTPFromAPasswordOnlySessionDoesNotStamp(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "enrol-nostamp@example.test")
	id, cookie, err := w.sessions.CreatePasswordOnly(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	w.setLastSignIn(t, uid, old)
	if _, newCookie, _, err := w.enrol.ConfirmTOTP(ctx, uid, id, enrol(t, w.totp, w.clock, uid)); err != nil {
		t.Fatal(err)
	} else if newCookie == "" || newCookie == cookie {
		t.Fatalf("cookie = %q, want a replaced one", newCookie)
	}
	got := w.lastSignIn(t, uid)
	if got == nil || !got.Equal(old) {
		t.Fatalf("last_sign_in_at = %v after enrolling, want unchanged %v", got, old)
	}
	if n := w.activeSessions(t, uid); n != 1 {
		t.Fatalf("active_sessions = %d, want 1: the enrolling session is complete", n)
	}
}
