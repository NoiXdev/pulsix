// A session opened complete on a password alone because the second factor
// is optional (PULSIX_MFA_OPTIONAL) is marked password-only. That proof is
// only good while the account has no factor: once it has one, the session
// reads as pending and stale until a real factor proof clears the mark.
// These tests drive the real database.
package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// addFactor gives uid a second factor of the given kind straight in the
// database, the way a concurrent enrolment in another session commits one.
// "pending totp" is a TOTP secret that was never confirmed: not a factor.
func (w *resetWorld) addFactor(t *testing.T, uid uuid.UUID, kind string) {
	t.Helper()
	var err error
	switch kind {
	case "totp":
		_, err = w.d.Pool().Exec(ctx, `INSERT INTO totp_secrets (user_id, secret_sealed, confirmed_at) VALUES ($1, '\x00', now())`, uid)
	case "pending totp":
		_, err = w.d.Pool().Exec(ctx, `INSERT INTO totp_secrets (user_id, secret_sealed) VALUES ($1, '\x00')`, uid)
	case "passkey":
		_, err = w.d.Pool().Exec(ctx,
			`INSERT INTO passkeys (id, user_id, name, credential_id, public_key, attestation) VALUES ($1, $2, 'key', $3, '\x01', 'none')`,
			uuid.Must(uuid.NewV7()), uid, []byte("cred-"+uid.String()))
	default:
		t.Fatalf("unknown factor kind %q", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func (w *resetWorld) lookup(t *testing.T, cookie string) auth.Principal {
	t.Helper()
	p, err := w.sessions.Lookup(ctx, cookie)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	return p
}

func TestPasswordOnlySessionIsPendingOnceTheAccountHasAFactor(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		superseded bool
	}{
		{kind: "totp", superseded: true},
		{kind: "passkey", superseded: true},
		{kind: "pending totp", superseded: false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "password-only@example.test")
			id, cookie, err := w.sessions.CreatePasswordOnly(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			p := w.lookup(t, cookie)
			if !p.MFAComplete || !p.PasswordOnly || !p.FactorFresh(time.Now()) || p.SessionID != id {
				t.Fatalf("fresh password-only session = %+v, want complete, password-only and fresh", p)
			}

			w.addFactor(t, uid, tc.kind)

			p = w.lookup(t, cookie)
			if p.MFAComplete == tc.superseded {
				t.Fatalf("after the factor: complete = %v, want %v", p.MFAComplete, !tc.superseded)
			}
			if p.FactorFresh(time.Now()) == tc.superseded {
				t.Fatalf("after the factor: fresh = %v (mfa_at %v), want %v", p.FactorFresh(time.Now()), p.MFAAt, !tc.superseded)
			}
			complete, err := w.sessions.StillValid(ctx, id)
			if err != nil || complete == tc.superseded {
				t.Fatalf("StillValid = %v, %v; want complete %v", complete, err, !tc.superseded)
			}
			if !tc.superseded {
				return
			}

			// A real factor proof (TOTP verify, MarkMFAComplete) clears the
			// mark: the session is complete and fresh again.
			newCookie, err := w.sessions.MarkMFAComplete(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			p = w.lookup(t, newCookie)
			if !p.MFAComplete || p.PasswordOnly || !p.FactorFresh(time.Now()) {
				t.Fatalf("after the factor proof = %+v, want complete, fresh and no longer password-only", p)
			}
		})
	}
}

// Mode off (or a session completed by a real factor): a factor that appears
// later changes nothing.
func TestCompleteSessionStaysCompleteWhenAFactorIsAdded(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "factor-complete@example.test")
	_, cookie, err := w.sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	w.addFactor(t, uid, "passkey")
	if p := w.lookup(t, cookie); !p.MFAComplete || p.PasswordOnly || !p.FactorFresh(time.Now()) {
		t.Fatalf("session = %+v, want complete and fresh", p)
	}
}

// The password step-up refreshes the proof only while the account has no
// factor, decided in the same statement that writes it.
func TestRefreshPasswordProof(t *testing.T) {
	t.Run("no factor", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "refresh@example.test")
		id, cookie, err := w.sessions.CreatePasswordOnly(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		w.age(t, id)
		newCookie, err := w.enrol.RefreshPasswordProof(ctx, uid, id)
		if err != nil {
			t.Fatalf("RefreshPasswordProof: %v", err)
		}
		if _, err := w.sessions.Lookup(ctx, cookie); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("old cookie: %v, want it rotated away", err)
		}
		if p := w.lookup(t, newCookie); !p.MFAComplete || !p.PasswordOnly || !p.FactorFresh(time.Now()) {
			t.Fatalf("refreshed = %+v, want complete, fresh and still password-only", p)
		}
	})
	t.Run("factor present at the update", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "refresh-factor@example.test")
		id, cookie, err := w.sessions.CreatePasswordOnly(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		w.age(t, id)
		w.addFactor(t, uid, "passkey")
		if _, err := w.enrol.RefreshPasswordProof(ctx, uid, id); !errors.Is(err, auth.ErrPasswordNotTheFactor) {
			t.Fatalf("RefreshPasswordProof: %v, want ErrPasswordNotTheFactor", err)
		}
		// Nothing was written: the old cookie still names the session, and
		// its proof is as stale as before.
		var fresh bool
		if err := w.d.Pool().QueryRow(ctx, `SELECT mfa_at > now() - interval '1 minute' FROM sessions WHERE id = $1`, id).Scan(&fresh); err != nil {
			t.Fatal(err)
		}
		if fresh {
			t.Fatal("mfa_at was refreshed for an account with a factor")
		}
		if p := w.lookup(t, cookie); p.MFAComplete {
			t.Fatalf("session = %+v, want pending", p)
		}
	})
	t.Run("ended session", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "refresh-ended@example.test")
		id, _, err := w.sessions.CreatePasswordOnly(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.sessions.Revoke(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := w.enrol.RefreshPasswordProof(ctx, uid, id); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("RefreshPasswordProof: %v, want ErrNotFound", err)
		}
	})
}

// A renewal by password keeps the mark; one by a factor clears it.
func TestRenewalCarriesThePasswordOnlyMark(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "renew-mark@example.test")
	id, _, err := w.sessions.CreatePasswordOnly(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	byPassword, err := w.enrol.RenewSessionByPassword(ctx, uid, id)
	if err != nil {
		t.Fatalf("RenewSessionByPassword: %v", err)
	}
	if p := w.lookup(t, byPassword.Cookie); !p.MFAComplete || !p.PasswordOnly {
		t.Fatalf("renewed by password = %+v, want complete and password-only", p)
	}
	byFactor, err := w.enrol.RenewSession(ctx, uid, byPassword.ID)
	if err != nil {
		t.Fatalf("RenewSession: %v", err)
	}
	if p := w.lookup(t, byFactor.Cookie); !p.MFAComplete || p.PasswordOnly {
		t.Fatalf("renewed by a factor = %+v, want complete and not password-only", p)
	}

	// A renewal by password that races a first enrolment still yields a
	// session that must verify the new factor.
	w.addFactor(t, uid, "totp")
	late, err := w.enrol.RenewSessionByPassword(ctx, uid, byFactor.ID)
	if err != nil {
		t.Fatalf("RenewSessionByPassword: %v", err)
	}
	if p := w.lookup(t, late.Cookie); p.MFAComplete {
		t.Fatalf("renewed by password after a factor appeared = %+v, want pending", p)
	}
}

// age pushes a session's proof past the step-up window.
func (w *resetWorld) age(t *testing.T, id uuid.UUID) {
	t.Helper()
	if _, err := w.d.Pool().Exec(ctx, `UPDATE sessions SET mfa_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}
