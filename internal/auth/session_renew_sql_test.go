// Renewing a session (backlog 3.94) replaces it: a new MFA-complete session
// with a fresh lifetime, and the old one revoked, in one transaction that
// takes the account row first. These tests drive the real database.
package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// openSession opens an MFA-complete session for uid with its proof aged
// past the step-up window, and returns its id and cookie.
func (w *resetWorld) openSession(t *testing.T, uid uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	id, cookie, err := w.sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Pool().Exec(ctx, `UPDATE sessions SET mfa_at = now() - interval '1 hour', expires_at = now() + interval '3 minutes' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	return id, cookie
}

func (w *resetWorld) liveSessions(t *testing.T, uid uuid.UUID) int {
	t.Helper()
	var n int
	if err := w.d.Pool().QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRenewSessionReplacesTheSession(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "renew@example.test")
	oldID, oldCookie := w.openSession(t, uid)

	start := time.Now()
	renewed, err := w.enrol.RenewSession(ctx, uid, oldID)
	if err != nil {
		t.Fatalf("RenewSession: %v", err)
	}
	if renewed.ID == oldID || renewed.ID == uuid.Nil {
		t.Fatalf("renewed id %s, want a new session (old %s)", renewed.ID, oldID)
	}
	if _, err := w.sessions.Lookup(ctx, oldCookie); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("old cookie after renewal: %v, want ErrUnauthenticated", err)
	}
	p, err := w.sessions.Lookup(ctx, renewed.Cookie)
	if err != nil {
		t.Fatalf("new cookie does not resolve: %v", err)
	}
	if p.SessionID != renewed.ID || p.UserID != uid || !p.MFAComplete {
		t.Fatalf("new session = %+v, want complete session %s of %s", p, renewed.ID, uid)
	}
	if !p.FactorFresh(time.Now()) {
		t.Fatalf("new session mfa_at = %v, want now", p.MFAAt)
	}
	wantExp := start.Add(auth.SessionTTL)
	if d := renewed.ExpiresAt.Sub(wantExp); d < -time.Minute || d > time.Minute {
		t.Fatalf("renewed expiry %v, want about %v", renewed.ExpiresAt, wantExp)
	}
	if d := p.ExpiresAt.Sub(renewed.ExpiresAt); d < -time.Millisecond || d > time.Millisecond {
		t.Fatalf("stored expiry %v differs from the reported %v", p.ExpiresAt, renewed.ExpiresAt)
	}
	if n := w.liveSessions(t, uid); n != 1 {
		t.Fatalf("live sessions = %d, want 1", n)
	}
}

// A session that is not live, belongs to another account, or is locked is
// never replaced: ErrNotFound, and no new session.
func TestRenewSessionRefusesASessionItCannotReplace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, w *resetWorld, uid, sid uuid.UUID) uuid.UUID
	}{
		{"revoked", func(t *testing.T, w *resetWorld, uid, sid uuid.UUID) uuid.UUID {
			if err := w.sessions.Revoke(ctx, sid); err != nil {
				t.Fatal(err)
			}
			return uid
		}},
		{"expired", func(t *testing.T, w *resetWorld, uid, sid uuid.UUID) uuid.UUID {
			if _, err := w.d.Pool().Exec(ctx, `UPDATE sessions SET expires_at = now() - interval '1 second' WHERE id = $1`, sid); err != nil {
				t.Fatal(err)
			}
			return uid
		}},
		{"another account's", func(t *testing.T, w *resetWorld, _, _ uuid.UUID) uuid.UUID {
			return w.seedAccount(t, "renew-other@example.test")
		}},
		{"locked", func(t *testing.T, w *resetWorld, uid, sid uuid.UUID) uuid.UUID {
			if _, err := w.q.LockSession(ctx, sid); err != nil {
				t.Fatal(err)
			}
			return uid
		}},
		{"unknown", func(t *testing.T, w *resetWorld, uid, _ uuid.UUID) uuid.UUID {
			return uid
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "renew-refused@example.test")
			sid, _ := w.openSession(t, uid)
			caller := tc.setup(t, w, uid, sid)
			target := sid
			if tc.name == "unknown" {
				target = uuid.Must(uuid.NewV7())
			}
			before := w.sessionCount(t, uid) + w.sessionCount(t, caller)
			if _, err := w.enrol.RenewSession(ctx, caller, target); !errors.Is(err, auth.ErrNotFound) {
				t.Fatalf("RenewSession = %v, want ErrNotFound", err)
			}
			if after := w.sessionCount(t, uid) + w.sessionCount(t, caller); after != before {
				t.Fatalf("session rows %d -> %d, want no new session", before, after)
			}
		})
	}
}

// A locked session stays as it was when a renewal is refused: still live,
// still locked.
func TestRenewSessionLeavesALockedSessionAlone(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "renew-locked@example.test")
	sid, cookie := w.openSession(t, uid)
	if _, err := w.q.LockSession(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := w.enrol.RenewSession(ctx, uid, sid); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("RenewSession = %v, want ErrNotFound", err)
	}
	p, err := w.sessions.Lookup(ctx, cookie)
	if err != nil || !p.Locked {
		t.Fatalf("locked session after a refused renewal: %+v, %v; want still live and locked", p, err)
	}
}

// A session is replaced once: a second renewal of the same old session
// (a double submit, a second tab) finds it revoked.
func TestRenewSessionIsSingleUse(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "renew-once@example.test")
	sid, _ := w.openSession(t, uid)
	if _, err := w.enrol.RenewSession(ctx, uid, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := w.enrol.RenewSession(ctx, uid, sid); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("second RenewSession = %v, want ErrNotFound", err)
	}
	if n := w.liveSessions(t, uid); n != 1 {
		t.Fatalf("live sessions = %d, want 1", n)
	}
}

// Two renewals of one session racing each other: exactly one wins.
func TestRenewSessionConcurrentRenewalsReplaceOnce(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "renew-race@example.test")
	sid, _ := w.openSession(t, uid)
	const n = 4
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := w.enrol.RenewSession(ctx, uid, sid)
			errs <- err
		}()
	}
	won := 0
	for range n {
		err := <-errs
		switch {
		case err == nil:
			won++
		case errors.Is(err, auth.ErrNotFound):
		default:
			t.Errorf("RenewSession: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("renewals that succeeded = %d, want 1", won)
	}
	if live := w.liveSessions(t, uid); live != 1 {
		t.Fatalf("live sessions = %d, want 1", live)
	}
}

// The renewal and an administrator's second-factor reset serialise on the
// account row: whichever goes second sees the other's result, and the
// account ends with no live session either way.
func TestRenewSessionAndSecondFactorResetSerialise(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "renew-reset@example.test")
	sid, _ := w.openSession(t, uid)
	done := make(chan error, 1)
	go func() {
		_, err := w.enrol.RenewSession(ctx, uid, sid)
		done <- err
	}()
	w.reset(t, uid)
	if err := <-done; err != nil && !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("RenewSession racing a reset: %v", err)
	}
	if n := w.liveSessions(t, uid); n != 0 {
		t.Fatalf("live sessions after the reset = %d, want 0", n)
	}
}
