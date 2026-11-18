// The follow-ups of the optional second factor's last security review
// (backlog 3.114): the password-only sign-in mark of an account that has a
// factor, the organization scope of a credential mint, and a pending
// session's first enrolment after the account gained a factor elsewhere.
// These tests drive the real database.
package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/noixdev/pulsix/internal/auth"
)

// A factor that commits after a login checked for one but before its
// password-only session is inserted leaves the account unmarked: the
// session is superseded from its first lookup and can mint nothing, so the
// mark would only make a later first factor (after an administrator's
// reset) revoke tokens created while the account had a factor.
func TestPasswordOnlySessionOfAnEnrolledAccountLeavesNoMark(t *testing.T) {
	for _, kind := range []string{"passkey", "totp"} {
		t.Run(kind, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "late-mark-"+kind+"@example.test")
			// The login checked: no factor. Then another session enrolled
			// one, and only then is the password-only session inserted.
			w.addFactor(t, uid, kind)
			if _, _, err := w.sessions.CreatePasswordOnly(ctx, uid); err != nil {
				t.Fatal(err)
			}
			if w.passwordOnlyMarked(t, uid) {
				t.Fatal("a password-only session of an enrolled account marked it")
			}

			// Tokens minted while the account had a factor survive the
			// first factor enrolled after an administrator's reset.
			live := w.seedTokens(t, uid)
			w.reset(t, uid)
			_, revoked, err := w.finishPasskey(t, w.session(t, uid, false))
			if err != nil || revoked {
				t.Fatalf("first factor after the reset = revoked %v, %v; want nothing revoked", revoked, err)
			}
			if n := live(); n != 2 {
				t.Fatalf("live tokens = %d, want 2", n)
			}
		})
	}
}

// markPasswordOnly sets uid's password-only sign-in mark straight in the
// database, the way a password-only session insert that raced the first
// factor's enrolment left it.
func (w *resetWorld) markPasswordOnly(t *testing.T, uid uuid.UUID) {
	t.Helper()
	if _, err := w.d.Pool().Exec(ctx, `UPDATE users SET password_only_signin_at = now() WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
}

// An administrator's second-factor reset drops a mark that sits beside a
// factor: the first factor takes the mark, so one beside a factor was left
// by a session insert that raced that enrolment and stands for nothing the
// password alone could mint. A mark on an account without a factor is a
// real password-only sign-in and stays.
func TestSecondFactorResetDropsAMarkBesideAFactor(t *testing.T) {
	for _, tc := range []struct {
		factor string
		want   bool
	}{
		{"passkey", false},
		{"totp", false},
		{"pending totp", true},
		{"", true},
	} {
		t.Run("factor "+tc.factor, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "reset-mark@example.test")
			if tc.factor != "" {
				w.addFactor(t, uid, tc.factor)
			}
			w.markPasswordOnly(t, uid)
			w.reset(t, uid)
			if got := w.passwordOnlyMarked(t, uid); got != tc.want {
				t.Fatalf("marked after the reset = %v, want %v", got, tc.want)
			}
		})
	}
}

// seedOrg creates an organization and returns its id.
func (w *resetWorld) seedOrg(t *testing.T, slug string) uuid.UUID {
	t.Helper()
	org := uuid.Must(uuid.NewV7())
	if _, err := w.d.Pool().Exec(ctx, `INSERT INTO organizations (id, slug, name) VALUES ($1, $2, $2)`, org, slug); err != nil {
		t.Fatal(err)
	}
	return org
}

// WithCompleteSession runs its write in a transaction bound to the
// organization it names, so row-level security checks the insert: a token
// of another organization is refused there, and one of its own goes in.
func TestWithCompleteSessionRunsInTheOrganization(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "org-mint@example.test")
	p := w.session(t, uid, true)
	own, other := w.seedOrg(t, "mint-own"), w.seedOrg(t, "mint-other")
	tokens := auth.NewTokens(auth.NewSQLTokenStore(w.d), time.Now)
	mint := func(org uuid.UUID) error {
		return w.enrol.WithCompleteSession(ctx, own, uid, p.SessionID, func(ctx context.Context) error {
			_, _, err := tokens.CreateWith(ctx, uid, org, auth.NewToken{Name: "ci", Role: "owner"})
			return err
		})
	}
	var pgErr *pgconn.PgError
	if err := mint(other); !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("a token of another organization: %v, want SQLSTATE 42501 (row-level security)", err)
	}
	if err := mint(own); err != nil {
		t.Fatalf("a token of the organization itself: %v", err)
	}
	var n int
	if err := w.d.Pool().QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("tokens = %d, want only the organization's own", n)
	}
}

// A pending session that is not password-only may enrol the account's
// first factor, but not once another session has enrolled one: the account
// then has a factor this session has not proven, and enrolling a second
// must not stand in for proving it. Nothing is stored.
func TestPendingEnrolmentRefusedOnceTheAccountIsEnrolled(t *testing.T) {
	t.Run("passkey", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "pending-passkey@example.test")
		p := w.session(t, uid, false)
		w.addFactor(t, uid, "passkey")
		if _, _, err := w.finishPasskey(t, p); !errors.Is(err, auth.ErrSessionNotComplete) {
			t.Fatalf("FinishPasskey: %v, want ErrSessionNotComplete", err)
		}
		if n := w.passkeyCount(t, uid); n != 1 {
			t.Fatalf("passkeys = %d, want only the one that was there", n)
		}
	})
	t.Run("totp", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "pending-totp@example.test")
		p := w.session(t, uid, false)
		code := enrol(t, w.totp, w.clock, uid)
		w.addFactor(t, uid, "passkey")
		if _, _, _, err := w.enrol.ConfirmTOTP(ctx, uid, p.SessionID, code); !errors.Is(err, auth.ErrSessionNotComplete) {
			t.Fatalf("ConfirmTOTP: %v, want ErrSessionNotComplete", err)
		}
		if on, err := w.totp.Enabled(ctx, uid); err != nil || on {
			t.Fatalf("TOTP enabled = %v, %v; want the secret left unconfirmed", on, err)
		}
	})
	t.Run("first factor", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "pending-first@example.test")
		p := w.session(t, uid, false)
		cookie, _, err := w.finishPasskey(t, p)
		if err != nil {
			t.Fatalf("FinishPasskey: %v", err)
		}
		if got := w.lookup(t, cookie); !got.MFAComplete {
			t.Fatal("the enrolling session is not complete")
		}
	})
	t.Run("complete session adds a second factor", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "complete-second@example.test")
		w.addFactor(t, uid, "totp")
		if _, _, err := w.finishPasskey(t, w.session(t, uid, true)); err != nil {
			t.Fatalf("FinishPasskey: %v", err)
		}
		if n := w.passkeyCount(t, uid); n != 1 {
			t.Fatalf("passkeys = %d, want 1", n)
		}
	})
}
