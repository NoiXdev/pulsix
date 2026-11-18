// Recovery codes belong to a confirmed TOTP secret, and an account's first
// second factor closes what a password-only sign-in (PULSIX_MFA_OPTIONAL)
// left open: stray recovery codes, API tokens, and a factor enrolled by
// someone else in the meantime. These tests drive the real database.
package auth_test

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// plantRecoveryCode stores code as an unused recovery code of uid straight
// in the database, the way a regenerate without a confirmed secret did.
func (w *resetWorld) plantRecoveryCode(t *testing.T, uid uuid.UUID, code string) {
	t.Helper()
	sum := sha256.Sum256([]byte(code))
	if _, err := w.d.Pool().Exec(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1, $2)`, uid, sum[:]); err != nil {
		t.Fatal(err)
	}
}

func (w *resetWorld) recoveryCodes(t *testing.T, uid uuid.UUID) int {
	t.Helper()
	var n int
	if err := w.d.Pool().QueryRow(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedTokens gives uid an organization token and a personal token and
// returns a function that counts how many of them are still unrevoked.
func (w *resetWorld) seedTokens(t *testing.T, uid uuid.UUID) func() int {
	t.Helper()
	org := uuid.Must(uuid.NewV7())
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, slug, name) VALUES ($1, $2, 'Tokens')`, []any{org, "tokens-" + org.String()}},
		{`INSERT INTO api_tokens (id, user_id, org_id, name, role, secret_hash) VALUES ($1, $2, $3, 'ci', 'owner', $4)`,
			[]any{uuid.Must(uuid.NewV7()), uid, org, []byte("org-" + uid.String())}},
		{`INSERT INTO personal_tokens (id, user_id, name, secret_hash) VALUES ($1, $2, 'mine', $3)`,
			[]any{uuid.Must(uuid.NewV7()), uid, []byte("personal-" + uid.String())}},
	} {
		if _, err := w.d.Pool().Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	return func() int {
		t.Helper()
		var n int
		if err := w.d.Pool().QueryRow(ctx,
			`SELECT (SELECT count(*) FROM api_tokens WHERE user_id = $1 AND revoked_at IS NULL)
			      + (SELECT count(*) FROM personal_tokens WHERE user_id = $1 AND revoked_at IS NULL)`, uid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
}

// passwordOnlySession opens a password-only session for uid and resolves it.
func (w *resetWorld) passwordOnlySession(t *testing.T, uid uuid.UUID) auth.Principal {
	t.Helper()
	_, cookie, err := w.sessions.CreatePasswordOnly(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	return w.lookup(t, cookie)
}

// finishPasskey runs a whole passkey registration on p's session.
func (w *resetWorld) finishPasskey(t *testing.T, p auth.Principal) (string, bool, error) {
	t.Helper()
	opts, cid, err := w.passkeys.BeginRegistration(ctx, p.UserID)
	if err != nil {
		t.Fatal(err)
	}
	return w.enrol.FinishPasskey(ctx, p, cid, "Key", buildNoneAttestationResponse(t, opts, testRPID, testOrigin, randomCredentialID(t)))
}

// A recovery code redeems only against a confirmed TOTP secret: one stored
// beside a secret that was never confirmed proves nothing.
func TestVerifyRecoveryNeedsAConfirmedSecret(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "recovery-pending@example.test")
	w.addFactor(t, uid, "pending totp")
	w.plantRecoveryCode(t, uid, "aaaaa-bbbbb-ccccc")
	if ok, err := w.totp.VerifyRecovery(ctx, uid, "aaaaa-bbbbb-ccccc"); ok || err != nil {
		t.Fatalf("VerifyRecovery with an unconfirmed secret = %v, %v; want false, nil", ok, err)
	}
	if n := w.recoveryCodes(t, uid); n != 1 {
		t.Fatalf("recovery codes = %d, want the refused one left unused", n)
	}

	if _, err := w.d.Pool().Exec(ctx, `UPDATE totp_secrets SET confirmed_at = now() WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.totp.VerifyRecovery(ctx, uid, "aaaaa-bbbbb-ccccc"); !ok || err != nil {
		t.Fatalf("VerifyRecovery with a confirmed secret = %v, %v; want true", ok, err)
	}
}

// Regenerating needs a confirmed TOTP secret too: there is nothing for the
// codes to stand in for otherwise.
func TestRegenerateRecoveryCodesNeedsAConfirmedSecret(t *testing.T) {
	for _, kind := range []string{"none", "pending totp", "passkey"} {
		t.Run(kind, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "regenerate@example.test")
			if kind != "none" {
				w.addFactor(t, uid, kind)
			}
			if _, err := w.totp.RegenerateRecoveryCodes(ctx, uid); !errors.Is(err, auth.ErrTOTPNotEnabled) {
				t.Fatalf("RegenerateRecoveryCodes: %v, want ErrTOTPNotEnabled", err)
			}
			if n := w.recoveryCodes(t, uid); n != 0 {
				t.Fatalf("recovery codes = %d, want none", n)
			}
		})
	}
}

// A passkey that is the account's first factor removes any recovery codes
// left over; one added beside a confirmed TOTP secret keeps that secret's.
func TestFinishPasskeyFirstFactorRemovesStrayRecoveryCodes(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "stray-codes@example.test")
	w.addFactor(t, uid, "pending totp")
	w.plantRecoveryCode(t, uid, "stray-one")
	if _, _, err := w.finishPasskey(t, w.session(t, uid, false)); err != nil {
		t.Fatalf("FinishPasskey: %v", err)
	}
	if n := w.recoveryCodes(t, uid); n != 0 {
		t.Fatalf("recovery codes after the first factor = %d, want none", n)
	}

	other := w.seedAccount(t, "kept-codes@example.test")
	w.addFactor(t, other, "totp")
	w.plantRecoveryCode(t, other, "kept-one")
	if _, _, err := w.finishPasskey(t, w.session(t, other, true)); err != nil {
		t.Fatalf("FinishPasskey: %v", err)
	}
	if n := w.recoveryCodes(t, other); n != 1 {
		t.Fatalf("recovery codes beside a confirmed secret = %d, want 1", n)
	}
}

// A first factor enrolled from a password-only session revokes every token
// the account holds: whatever the password alone minted does not outlive
// the factor. Any other enrolment leaves them alone.
func TestFirstFactorFromAPasswordOnlySessionRevokesTokens(t *testing.T) {
	t.Run("passkey", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "revoke-passkey@example.test")
		live := w.seedTokens(t, uid)
		_, revoked, err := w.finishPasskey(t, w.passwordOnlySession(t, uid))
		if err != nil || !revoked {
			t.Fatalf("FinishPasskey = revoked %v, %v; want revoked", revoked, err)
		}
		if n := live(); n != 0 {
			t.Fatalf("live tokens = %d, want none", n)
		}
	})
	t.Run("totp", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "revoke-totp@example.test")
		live := w.seedTokens(t, uid)
		p := w.passwordOnlySession(t, uid)
		_, _, revoked, err := w.enrol.ConfirmTOTP(ctx, uid, p.SessionID, enrol(t, w.totp, w.clock, uid))
		if err != nil || !revoked {
			t.Fatalf("ConfirmTOTP = revoked %v, %v; want revoked", revoked, err)
		}
		if n := live(); n != 0 {
			t.Fatalf("live tokens = %d, want none", n)
		}
	})
	t.Run("not password-only", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "keep-tokens@example.test")
		live := w.seedTokens(t, uid)
		_, revoked, err := w.finishPasskey(t, w.session(t, uid, true))
		if err != nil || revoked {
			t.Fatalf("FinishPasskey = revoked %v, %v; want nothing revoked", revoked, err)
		}
		if n := live(); n != 2 {
			t.Fatalf("live tokens = %d, want 2", n)
		}
	})
	t.Run("second factor", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "second-factor@example.test")
		p := w.passwordOnlySession(t, uid)
		if _, _, _, err := w.enrol.ConfirmTOTP(ctx, uid, p.SessionID, enrol(t, w.totp, w.clock, uid)); err != nil {
			t.Fatal(err)
		}
		live := w.seedTokens(t, uid)
		// A passkey added after the TOTP secret is a second factor and
		// revokes nothing.
		_, revoked, err := w.finishPasskey(t, w.session(t, uid, true))
		if err != nil || revoked {
			t.Fatalf("FinishPasskey = revoked %v, %v; want nothing revoked", revoked, err)
		}
		if n := live(); n != 2 {
			t.Fatalf("live tokens = %d, want 2", n)
		}
	})
}

// A password-only session's enrolment re-checks, under the account's lock,
// that the account still has no factor: one that appeared after the request
// resolved its session refuses it and nothing is stored.
func TestPasswordOnlyEnrolmentRefusedOnceAFactorExists(t *testing.T) {
	t.Run("passkey", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "late-passkey@example.test")
		p := w.passwordOnlySession(t, uid)
		w.addFactor(t, uid, "passkey")
		if _, _, err := w.finishPasskey(t, p); !errors.Is(err, auth.ErrPasswordNotTheFactor) {
			t.Fatalf("FinishPasskey: %v, want ErrPasswordNotTheFactor", err)
		}
		if n := w.passkeyCount(t, uid); n != 1 {
			t.Fatalf("passkeys = %d, want only the one that was there", n)
		}
	})
	t.Run("totp", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "late-totp@example.test")
		p := w.passwordOnlySession(t, uid)
		code := enrol(t, w.totp, w.clock, uid)
		w.addFactor(t, uid, "passkey")
		if _, _, _, err := w.enrol.ConfirmTOTP(ctx, uid, p.SessionID, code); !errors.Is(err, auth.ErrPasswordNotTheFactor) {
			t.Fatalf("ConfirmTOTP: %v, want ErrPasswordNotTheFactor", err)
		}
		if on, err := w.totp.Enabled(ctx, uid); err != nil || on {
			t.Fatalf("TOTP enabled = %v, %v; want the secret left unconfirmed", on, err)
		}
	})
}
