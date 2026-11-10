// A passkey login validates its assertion against a credential it reads
// before it opens a session. An administrator's second-factor reset removes
// every passkey of the account and ends every session of it, so a login
// whose assertion was checked before the reset committed must not open a
// session after it: the session is created in one instance transaction
// that takes the account row the reset locks and re-checks the credential
// under it (backlog 7.109, variant b). The same holds for beginning a TOTP
// enrolment on a session that has ended. These tests drive the real
// database and the real reset, as enrolment_reset_sql_test.go does.
package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// hookedSessions is the real session store with a seam in front of the
// account lock, the first thing a login does once its assertion has been
// validated. beforeLock runs once, on the first LockAccount; afterLock runs
// once, inside the login's transaction while it holds the lock.
type hookedSessions struct {
	*auth.Sessions
	once       sync.Once
	beforeLock func()
	afterLock  func(ctx context.Context)
	// created counts the calls that reached Create, the seam's view of
	// whether the login went on to open a session at all.
	created int
}

func (h *hookedSessions) LockAccount(ctx context.Context, userID uuid.UUID) error {
	first := false
	h.once.Do(func() { first = true })
	if first && h.beforeLock != nil {
		h.beforeLock()
	}
	if err := h.Sessions.LockAccount(ctx, userID); err != nil {
		return err
	}
	if first && h.afterLock != nil {
		h.afterLock(ctx)
	}
	return nil
}

func (h *hookedSessions) Create(ctx context.Context, userID uuid.UUID, mfaComplete bool) (uuid.UUID, string, error) {
	h.created++
	return h.Sessions.Create(ctx, userID, mfaComplete)
}

// loginKey is a passkey registered for an account, with the private key a
// test signs its assertions with.
type loginKey struct {
	credID []byte
	priv   *ecdsa.PrivateKey
}

// seedLoginKey registers a passkey for uid whose assertions the test can
// sign (buildSignedAssertionResponse, passkey_clone_test.go).
func (w *resetWorld) seedLoginKey(t *testing.T, uid uuid.UUID) loginKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k := loginKey{credID: randomCredentialID(t), priv: priv}
	if err := auth.NewSQLPasskeyStore(w.q).AddCredential(ctx, uid, "Login key", auth.Credential{
		ID: uuid.Must(uuid.NewV7()), CredentialID: k.credID, PublicKey: buildCOSEPublicKey(t, &priv.PublicKey), Attestation: "none",
	}); err != nil {
		t.Fatal(err)
	}
	return k
}

// loginPasskeys is the world's passkey service with sessions in place of
// the real session store, so a test can put a seam in front of it.
func (w *resetWorld) loginPasskeys(t *testing.T, sessions auth.LoginSessions) *auth.Passkeys {
	t.Helper()
	pk, err := auth.NewPasskeys(auth.NewSQLPasskeyStore(w.q), auth.NewSQLStore(w.q), sessions, w.d, testRPID, "Pulsix Test", testOrigin, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return pk
}

// beginLogin starts a login ceremony on pk and signs its assertion with k
// as uid's passkey.
func beginLogin(t *testing.T, pk *auth.Passkeys, uid uuid.UUID, k loginKey) (uuid.UUID, []byte) {
	t.Helper()
	opts, cid, err := pk.BeginLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return cid, buildSignedAssertionResponse(t, opts, testRPID, testOrigin, k.credID, uid[:], 1, k.priv)
}

func (w *resetWorld) sessionCount(t *testing.T, uid uuid.UUID) int {
	t.Helper()
	var n int
	if err := w.d.Pool().QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The race the fix closes: the login validates its assertion against the
// passkey, the reset then commits (removing the passkey and ending every
// session), and only then does the login open its session. The login must
// fail exactly as one with an unregistered credential does, so the client
// cannot tell the race apart, and leave no session row behind.
func TestPasskeyLoginAfterSecondFactorResetOpensNoSession(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "login-reset@example.test")
	k := w.seedLoginKey(t, uid)

	// What a login with a credential nobody registered gets today.
	stranger := loginKey{credID: randomCredentialID(t), priv: k.priv}
	cid, response := beginLogin(t, w.passkeys, uid, stranger)
	_, unknownErr := w.passkeys.FinishLogin(ctx, cid, response)
	if !errors.Is(unknownErr, auth.ErrInvalidCredentials) {
		t.Fatalf("login with an unregistered credential = %v, want ErrInvalidCredentials", unknownErr)
	}

	sessions := &hookedSessions{Sessions: w.sessions}
	sessions.beforeLock = func() { w.reset(t, uid) }
	pk := w.loginPasskeys(t, sessions)
	cid, response = beginLogin(t, pk, uid, k)
	cookie, err := pk.FinishLogin(ctx, cid, response)
	if err == nil {
		t.Fatal("a login checked before the reset opened a session after it")
	}
	if !errors.Is(err, auth.ErrInvalidCredentials) || err.Error() != unknownErr.Error() {
		t.Errorf("error = %v, want exactly what an unregistered credential gets (%v)", err, unknownErr)
	}
	if cookie != "" {
		t.Error("a refused login handed out a session cookie")
	}
	if n := w.sessionCount(t, uid); n != 0 {
		t.Fatalf("sessions of the account after the refused login = %d, want 0", n)
	}
	if sessions.created != 0 {
		t.Errorf("the login went on to create a session %d time(s) after its credential was gone", sessions.created)
	}
}

// Without a reset the same login still opens an MFA-complete session and
// advances the credential's counter.
func TestPasskeyLoginOpensACompleteSession(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "login-ok@example.test")
	k := w.seedLoginKey(t, uid)
	cid, response := beginLogin(t, w.passkeys, uid, k)
	cookie, err := w.passkeys.FinishLogin(ctx, cid, response)
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	p, err := w.sessions.Lookup(ctx, cookie)
	if err != nil {
		t.Fatalf("the login's cookie does not resolve: %v", err)
	}
	if p.UserID != uid || !p.MFAComplete {
		t.Errorf("session = user %s, complete %v; want %s, true", p.UserID, p.MFAComplete, uid)
	}
	var count int64
	if err := w.d.Pool().QueryRow(ctx, `SELECT sign_count FROM passkeys WHERE credential_id = $1`, k.credID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("sign_count = %d, want 1", count)
	}
}

// The login and the reset serialise on the account row, in either order.
// A login that holds the row makes the reset wait, and the reset then ends
// the session it opened; a reset that holds the row makes the login wait,
// and the login then finds its credential gone and opens nothing. Neither
// order deadlocks.
func TestPasskeyLoginAndSecondFactorResetSerialise(t *testing.T) {
	t.Run("login first", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "login-first@example.test")
		k := w.seedLoginKey(t, uid)
		resetDone := make(chan error, 1)
		held := false
		sessions := &hookedSessions{Sessions: w.sessions}
		sessions.afterLock = func(ctx context.Context) {
			held = true
			go func() {
				_, err := w.accounts.ResetSecondFactor(context.Background(), true, w.admin, uid)
				resetDone <- err
			}()
			waitForLockWait(lockWaitCtx(t), t, backendPID(ctx, t, w.d))
			select {
			case err := <-resetDone:
				t.Errorf("the reset finished while a login held the account: %v", err)
			default:
			}
		}
		pk := w.loginPasskeys(t, sessions)
		cid, response := beginLogin(t, pk, uid, k)
		cookie, err := pk.FinishLogin(ctx, cid, response)
		if err != nil {
			t.Fatalf("FinishLogin while the reset waited: %v", err)
		}
		if !held {
			t.Fatal("the login opened its session without taking the account lock")
		}
		if err := <-resetDone; err != nil {
			t.Fatalf("ResetSecondFactor: %v", err)
		}
		if _, err := w.sessions.Lookup(ctx, cookie); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("the login's session after the reset: Lookup = %v, want ErrUnauthenticated", err)
		}
		if n := w.passkeyCount(t, uid); n != 0 {
			t.Fatalf("passkeys after the reset = %d, want 0", n)
		}
	})

	t.Run("reset first", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "reset-first-login@example.test")
		k := w.seedLoginKey(t, uid)
		cid, response := beginLogin(t, w.passkeys, uid, k)
		// The reset's own steps, held open: lock the account, remove its
		// passkeys, end its sessions.
		tx, err := w.d.Pool().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		for _, stmt := range []string{
			`SELECT 1 FROM users WHERE id = $1 FOR UPDATE`,
			`DELETE FROM passkeys WHERE user_id = $1`,
			`UPDATE sessions SET revoked_at = now() WHERE user_id = $1`,
		} {
			if _, err := tx.Exec(ctx, stmt, uid); err != nil {
				t.Fatal(err)
			}
		}
		type result struct {
			cookie string
			err    error
		}
		finished := make(chan result, 1)
		go func() {
			cookie, err := w.passkeys.FinishLogin(context.Background(), cid, response)
			finished <- result{cookie, err}
		}()
		waitForLockWait(lockWaitCtx(t), t, backendPID(ctx, t, tx))
		select {
		case r := <-finished:
			t.Fatalf("the login finished while the reset held the account: %v", r.err)
		default:
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		r := <-finished
		if !errors.Is(r.err, auth.ErrInvalidCredentials) {
			t.Fatalf("FinishLogin after the reset committed = %v, want ErrInvalidCredentials", r.err)
		}
		if r.cookie != "" {
			t.Error("a refused login handed out a session cookie")
		}
		if n := w.sessionCount(t, uid); n != 0 {
			t.Fatalf("sessions of the account after the refused login = %d, want 0", n)
		}
	})
}

// Beginning a TOTP enrolment on a session that has ended — signed out,
// expired, or ended by an administrator's second-factor reset after the
// request resolved it — is refused as ErrUnauthenticated and writes no
// pending secret (backlog 7.109).
func TestBeginEnrolOnEndedSessionWritesNothing(t *testing.T) {
	w := newResetWorld(t)
	for _, tc := range []struct {
		name    string
		session func(t *testing.T, uid uuid.UUID) uuid.UUID
	}{
		{name: "revoked", session: func(t *testing.T, uid uuid.UUID) uuid.UUID {
			return createSessionAt(t, w.q, uid, time.Now().Add(time.Hour), true)
		}},
		{name: "expired", session: func(t *testing.T, uid uuid.UUID) uuid.UUID {
			return createSessionAt(t, w.q, uid, time.Now().Add(-time.Minute), false)
		}},
		{name: "ended by a reset", session: func(t *testing.T, uid uuid.UUID) uuid.UUID {
			p := w.session(t, uid, true)
			w.reset(t, uid)
			return p.SessionID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid := w.seedAccount(t, "begin-enrol-"+uuid.NewString()+"@example.test")
			sid := tc.session(t, uid)
			secret, url, err := w.enrol.BeginTOTP(ctx, uid, sid, "user@example.test", "")
			if !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("BeginTOTP on a %s session = %v, want ErrUnauthenticated", tc.name, err)
			}
			if secret != "" || url != "" {
				t.Error("a refused enrolment handed out a secret")
			}
			var n int
			if err := w.d.Pool().QueryRow(ctx, `SELECT count(*) FROM totp_secrets WHERE user_id = $1`, uid).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("TOTP rows after the refused enrolment = %d, want 0", n)
			}
		})
	}
}

// On a live session BeginTOTP writes the pending secret as before.
func TestBeginTOTPOnALiveSessionWritesThePendingSecret(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "begin-live@example.test")
	p := w.session(t, uid, false)
	secret, url, err := w.enrol.BeginTOTP(ctx, uid, p.SessionID, "user@example.test", "")
	if err != nil {
		t.Fatalf("BeginTOTP: %v", err)
	}
	if secret == "" || url == "" {
		t.Fatal("BeginTOTP returned no secret")
	}
	code := codeAt(t, secret, w.clock)
	if _, _, err := w.enrol.ConfirmTOTP(ctx, uid, p.SessionID, code); err != nil {
		t.Fatalf("confirming the secret BeginTOTP wrote: %v", err)
	}
}

// Moving a confirmed factor needs a fresh code for it, and a wrong one
// spends a second-factor attempt (backlog 7.26). The liveness check must
// not roll that spend back with the refused enrolment.
func TestBeginTOTPWithAWrongCodeStillSpendsAnAttempt(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "begin-wrong@example.test")
	p := w.session(t, uid, false)
	if _, _, err := w.enrol.ConfirmTOTP(ctx, uid, p.SessionID, enrol(t, w.totp, w.clock, uid)); err != nil {
		t.Fatal(err)
	}
	live := w.session(t, uid, true)
	if _, _, err := w.enrol.BeginTOTP(ctx, uid, live.SessionID, "user@example.test", "not-a-code"); !errors.Is(err, auth.ErrInvalidTOTPCode) {
		t.Fatalf("BeginTOTP with a wrong code = %v, want ErrInvalidTOTPCode", err)
	}
	var attempts int
	if err := w.d.Pool().QueryRow(ctx, `SELECT failed_factor_attempts FROM totp_secrets WHERE user_id = $1`, uid).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("failed_factor_attempts = %d, want 1", attempts)
	}
}
