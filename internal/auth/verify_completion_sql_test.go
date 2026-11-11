package auth_test

import (
	"errors"
	"testing"

	"github.com/noixdev/pulsix/internal/auth"
)

// A verified code completes a pending, live session in the account-first
// order (backlog 7.118): the session becomes MFA-complete and carries a new
// cookie, the one it had before no longer resolving, and the sign-in is
// stamped. The serialisation with the reset is
// TestVerifiedSessionCompletionAndSecondFactorResetSerialise.
func TestCompleteVerifiedSessionUpgradesALiveSession(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "verify-live@example.test")
	_, oldCookie, err := w.sessions.Create(ctx, uid, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.sessions.Lookup(ctx, oldCookie)
	if err != nil {
		t.Fatal(err)
	}

	cookie, err := w.enrol.CompleteVerifiedSession(ctx, uid, p.SessionID)
	if err != nil {
		t.Fatalf("CompleteVerifiedSession = %v, want nil", err)
	}
	if cookie == "" || cookie == oldCookie {
		t.Fatalf("cookie = %q, want a new one", cookie)
	}
	got, err := w.sessions.Lookup(ctx, cookie)
	if err != nil {
		t.Fatalf("Lookup(new cookie) = %v", err)
	}
	if got.SessionID != p.SessionID || !got.MFAComplete {
		t.Fatalf("session after completion = %+v, want the same session, MFA-complete", got)
	}
	if _, err := w.sessions.Lookup(ctx, oldCookie); err == nil {
		t.Fatal("the cookie from before the completion still resolves")
	}
	var signedIn bool
	if err := w.d.Pool().QueryRow(ctx,
		`SELECT last_sign_in_at IS NOT NULL FROM users WHERE id = $1`, uid).Scan(&signedIn); err != nil {
		t.Fatal(err)
	}
	if !signedIn {
		t.Fatal("last_sign_in_at not stamped by completing a pending session")
	}
}

// A session that belongs to another account is not completed: the
// re-check under the lock refuses it as it refuses an ended one.
func TestCompleteVerifiedSessionRefusesAnotherAccountsSession(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "verify-owner@example.test")
	other := w.seedAccount(t, "verify-other@example.test")
	p := w.session(t, other, false)

	if _, err := w.enrol.CompleteVerifiedSession(ctx, uid, p.SessionID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("CompleteVerifiedSession on another account's session = %v, want ErrNotFound", err)
	}
	var complete bool
	if err := w.d.Pool().QueryRow(ctx,
		`SELECT mfa_complete FROM sessions WHERE id = $1`, p.SessionID).Scan(&complete); err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("another account's session was completed")
	}
}
