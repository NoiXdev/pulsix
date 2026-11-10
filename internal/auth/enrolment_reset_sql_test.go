// An administrator's second-factor reset removes every factor of the
// account and ends every session of it (accounts.Service.ResetSecondFactor).
// A request that resolved its session before the reset committed still
// carries that session's Principal when it reaches the service, so an
// enrolment begun before the reset must not be able to finish after it: the
// credential write and the session upgrade happen in one transaction, and
// both refuse a session that is revoked or gone (backlog 7.109). These
// tests drive the real database and the real reset, because the property
// is about what two transactions leave behind.
package auth_test

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// resetWorld wires the real enrolment, passkey, session and account
// administration services against one test database.
type resetWorld struct {
	d        *db.DB
	q        *gen.Queries
	enrol    *auth.Enrolment
	totp     *auth.TOTP
	passkeys *auth.Passkeys
	sessions *auth.Sessions
	accounts *accounts.Service
	clock    *mutableClock
	admin    uuid.UUID
}

// noMail is the accounts.Mailer of these tests: nothing is configured, so
// nobody is told about the reset.
type noMail struct{}

func (noMail) Send(context.Context, string, string, string) error { return nil }
func (noMail) Configured(context.Context) (bool, error)           { return false, nil }

func newResetWorld(t *testing.T) *resetWorld {
	t.Helper()
	d := testdb.Open(t)
	q := gen.New(d)
	var key [32]byte
	copy(key[:], "enrolment-reset-key-00000000001")
	clock := newMutableClock(time.Now().Truncate(30 * time.Second))
	tp := auth.NewTOTP(auth.NewSQLTOTPStore(d), crypto.NewBox(key), "Pulsix", clock.Now)
	sessions := auth.NewSessions(q, time.Now)
	pk, err := auth.NewPasskeys(auth.NewSQLPasskeyStore(q), auth.NewSQLStore(q), sessions, testRPID, "Pulsix Test", testOrigin, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	w := &resetWorld{
		d: d, q: q, totp: tp, passkeys: pk, sessions: sessions, clock: clock,
		enrol:    auth.NewEnrolment(tp, pk, sessions, d),
		accounts: accounts.NewService(accounts.NewSQLStore(d), noMail{}, "http://localhost", time.Now, rand.Reader),
	}
	w.admin = w.seedAccount(t, "reset-admin@example.test")
	return w
}

// seedAccount creates an account with a password, which the reset requires
// (an account whose invitation is pending is refused).
func (w *resetWorld) seedAccount(t *testing.T, email string) uuid.UUID {
	t.Helper()
	uid := seedReaperUser(t, w.q, email)
	if _, err := w.d.Pool().Exec(ctx, `UPDATE users SET password_hash = 'not-a-real-hash' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	return uid
}

// session opens a session for uid and resolves its Principal, the snapshot
// the cookie middleware takes when a request's headers arrive.
func (w *resetWorld) session(t *testing.T, uid uuid.UUID, mfaComplete bool) auth.Principal {
	t.Helper()
	_, cookie, err := w.sessions.Create(ctx, uid, mfaComplete)
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.sessions.Lookup(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (w *resetWorld) reset(t *testing.T, uid uuid.UUID) {
	t.Helper()
	if _, err := w.accounts.ResetSecondFactor(ctx, true, w.admin, uid); err != nil {
		t.Fatalf("ResetSecondFactor: %v", err)
	}
}

func (w *resetWorld) passkeyCount(t *testing.T, uid uuid.UUID) int {
	t.Helper()
	creds, err := w.passkeys.List(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	return len(creds)
}

// A passkey registration begun before the reset cannot finish after it,
// whether the session that began it was already complete (a factor proven
// within the step-up window) or pending only for want of a factor: the
// finish returns an error and the account has no passkey at all.
func TestPasskeyFinishAfterSecondFactorResetStoresNothing(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mfaComplete bool
		// withFactor seeds a passkey first: a complete session's account
		// has one, a pending enrolment session's account has none.
		withFactor bool
	}{
		{name: "complete session", mfaComplete: true, withFactor: true},
		{name: "pending enrolment session", mfaComplete: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "passkey-reset@example.test")
			if tc.withFactor {
				if err := auth.NewSQLPasskeyStore(w.q).AddCredential(ctx, uid, "Old key", auth.Credential{
					ID: uuid.Must(uuid.NewV7()), CredentialID: randomCredentialID(t), PublicKey: []byte("pk"), Attestation: "none",
				}); err != nil {
					t.Fatal(err)
				}
			}
			stale := w.session(t, uid, tc.mfaComplete)
			opts, cid, err := w.passkeys.BeginRegistration(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}

			w.reset(t, uid)

			response := buildNoneAttestationResponse(t, opts, testRPID, testOrigin, randomCredentialID(t))
			cookie, err := w.enrol.FinishPasskey(ctx, stale, cid, "Planted key", response)
			if err == nil {
				t.Fatal("finishing a registration on a session the reset ended returned no error")
			}
			if !errors.Is(err, auth.ErrUnauthenticated) {
				t.Errorf("error = %v, want ErrUnauthenticated (the session is gone)", err)
			}
			if cookie != "" {
				t.Errorf("a refused finish handed out a session cookie")
			}
			if n := w.passkeyCount(t, uid); n != 0 {
				t.Fatalf("passkeys after the reset = %d, want 0", n)
			}
		})
	}
}

// The same registration on a live session still works, and upgrades a
// pending enrolment session exactly as before: one call stores the passkey
// and completes the session with a new cookie, while a session that was
// already complete is left as it is.
func TestPasskeyFinishOnALiveSessionStoresAndUpgrades(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mfaComplete bool
		wantCookie  bool
	}{
		{name: "pending enrolment session", mfaComplete: false, wantCookie: true},
		{name: "complete session", mfaComplete: true, wantCookie: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "passkey-live@example.test")
			p := w.session(t, uid, tc.mfaComplete)
			opts, cid, err := w.passkeys.BeginRegistration(ctx, uid)
			if err != nil {
				t.Fatal(err)
			}
			response := buildNoneAttestationResponse(t, opts, testRPID, testOrigin, randomCredentialID(t))
			cookie, err := w.enrol.FinishPasskey(ctx, p, cid, "Key", response)
			if err != nil {
				t.Fatalf("FinishPasskey: %v", err)
			}
			if got := cookie != ""; got != tc.wantCookie {
				t.Fatalf("cookie returned = %v, want %v", got, tc.wantCookie)
			}
			if n := w.passkeyCount(t, uid); n != 1 {
				t.Fatalf("passkeys = %d, want 1", n)
			}
			if tc.wantCookie {
				up, err := w.sessions.Lookup(ctx, cookie)
				if err != nil {
					t.Fatalf("the new cookie does not resolve: %v", err)
				}
				if !up.MFAComplete {
					t.Error("the pending session must be complete once the passkey is stored")
				}
			}
		})
	}
}

// When the session upgrade fails, the passkey must not stand either: the
// two writes are one transaction. The session here ends between the
// Principal's snapshot and the finish by an ordinary sign-out.
func TestPasskeyFinishOnASignedOutSessionStoresNothing(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "passkey-signout@example.test")
	p := w.session(t, uid, false)
	opts, cid, err := w.passkeys.BeginRegistration(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.sessions.Revoke(ctx, p.SessionID); err != nil {
		t.Fatal(err)
	}
	response := buildNoneAttestationResponse(t, opts, testRPID, testOrigin, randomCredentialID(t))
	if _, err := w.enrol.FinishPasskey(ctx, p, cid, "Key", response); err == nil {
		t.Fatal("finishing on a signed-out session returned no error")
	}
	if n := w.passkeyCount(t, uid); n != 0 {
		t.Fatalf("passkeys = %d, want 0", n)
	}
}

// MarkMFAComplete must not complete a session that has ended: a revoked or
// expired session matches nothing and reports ErrNotFound, and stays as it
// was.
func TestMarkMFACompleteRefusesRevokedSession(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "mark-revoked@example.test")
	for _, tc := range []struct {
		name string
		id   uuid.UUID
	}{
		{name: "revoked", id: createSessionAt(t, w.q, uid, time.Now().Add(time.Hour), true)},
		{name: "expired", id: createSessionAt(t, w.q, uid, time.Now().Add(-time.Minute), false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := w.sessions.MarkMFAComplete(ctx, tc.id); !errors.Is(err, auth.ErrNotFound) {
				t.Fatalf("MarkMFAComplete on a %s session = %v, want ErrNotFound", tc.name, err)
			}
			var complete bool
			if err := w.d.Pool().QueryRow(ctx, `SELECT mfa_complete FROM sessions WHERE id = $1`, tc.id).Scan(&complete); err != nil {
				t.Fatal(err)
			}
			if complete {
				t.Errorf("the %s session was marked complete", tc.name)
			}
		})
	}
}

// TOTP's variant: once the reset has removed every factor, a stale
// Principal can begin a new enrolment (nothing to prove), but confirming it
// must fail on the ended session and leave no confirmed secret behind.
func TestConfirmTOTPAfterResetStoresNothing(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "totp-reset@example.test")
	stale := w.session(t, uid, true)

	w.reset(t, uid)

	code := enrol(t, w.totp, w.clock, uid)
	if _, _, err := w.enrol.ConfirmTOTP(ctx, uid, stale.SessionID, code); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("ConfirmTOTP on a session the reset ended = %v, want ErrNotFound", err)
	}
	confirmed, codes := confirmedAndCodes(t, w.q, w.d, uid)
	if confirmed || codes != 0 {
		t.Fatalf("after the refused confirmation: confirmed=%v, recovery codes=%d", confirmed, codes)
	}
}

// The two transactions serialise on the account row, in either order
// (backlog 7.109). An enrolment that holds the row makes the reset wait,
// and the reset then removes the passkey it stored; a reset that holds the
// row makes the enrolment wait, and the enrolment then finds its session
// ended and stores nothing. Either way no passkey outlives the reset.
func TestEnrolmentAndSecondFactorResetSerialise(t *testing.T) {
	// A pending session also stamps users.last_sign_in_at when it is
	// completed, an update of the very row the reset is queued for.
	for _, tc := range []struct {
		name        string
		mfaComplete bool
	}{
		{name: "enrolment first, complete session", mfaComplete: true},
		{name: "enrolment first, pending session", mfaComplete: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newResetWorld(t)
			uid := w.seedAccount(t, "enrolment-first@example.test")
			p := w.session(t, uid, tc.mfaComplete)
			resetDone := make(chan error, 1)
			err := w.d.WithInstanceTx(ctx, func(ctx context.Context) error {
				if err := w.sessions.LockLive(ctx, uid, p.SessionID); err != nil {
					return err
				}
				go func() {
					_, err := w.accounts.ResetSecondFactor(context.Background(), true, w.admin, uid)
					resetDone <- err
				}()
				waitForLockWait(lockWaitCtx(t), t, backendPID(ctx, t, w.d))
				select {
				case err := <-resetDone:
					t.Fatalf("the reset finished while an enrolment held the account: %v", err)
				default:
				}
				if err := auth.NewSQLPasskeyStore(w.q).AddCredential(ctx, uid, "Key", auth.Credential{
					ID: uuid.Must(uuid.NewV7()), CredentialID: randomCredentialID(t), PublicKey: []byte("pk"), Attestation: "none",
				}); err != nil {
					return err
				}
				if tc.mfaComplete {
					return nil
				}
				_, err := w.sessions.MarkMFAComplete(ctx, p.SessionID)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := <-resetDone; err != nil {
				t.Fatalf("ResetSecondFactor: %v", err)
			}
			if n := w.passkeyCount(t, uid); n != 0 {
				t.Fatalf("passkeys after the reset = %d, want 0", n)
			}
			var revoked bool
			if err := w.d.Pool().QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM sessions WHERE id = $1`, p.SessionID).Scan(&revoked); err != nil {
				t.Fatal(err)
			}
			if !revoked {
				t.Error("the reset must end the session the enrolment completed")
			}
		})
	}

	t.Run("reset first", func(t *testing.T) {
		w := newResetWorld(t)
		uid := w.seedAccount(t, "reset-first@example.test")
		p := w.session(t, uid, true)
		opts, cid, err := w.passkeys.BeginRegistration(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		// The reset's own steps, held open: lock the account, end its
		// sessions.
		tx, err := w.d.Pool().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, uid); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1`, uid); err != nil {
			t.Fatal(err)
		}
		response := buildNoneAttestationResponse(t, opts, testRPID, testOrigin, randomCredentialID(t))
		finished := make(chan error, 1)
		go func() {
			_, err := w.enrol.FinishPasskey(context.Background(), p, cid, "Key", response)
			finished <- err
		}()
		waitForLockWait(lockWaitCtx(t), t, backendPID(ctx, t, tx))
		select {
		case err := <-finished:
			t.Fatalf("the enrolment finished while the reset held the account: %v", err)
		default:
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-finished; !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("FinishPasskey after the reset committed = %v, want ErrUnauthenticated", err)
		}
		if n := w.passkeyCount(t, uid); n != 0 {
			t.Fatalf("passkeys after the reset = %d, want 0", n)
		}
	})
}

// The session can also end between the check at the start of the
// transaction and the upgrade at its end — a sign-out takes no account
// lock. The upgrade then matches nothing and the confirmed secret and its
// recovery codes roll back with it (backlogs 1.26, 7.109).
func TestConfirmTOTPRollsBackWhenTheSessionEndsMidway(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "totp-midway@example.test")
	p := w.session(t, uid, false)
	code := enrol(t, w.totp, w.clock, uid)

	// A sign-out, held open with the session row locked.
	tx, err := w.d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1`, p.SessionID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := w.enrol.ConfirmTOTP(context.Background(), uid, p.SessionID, code)
		done <- err
	}()
	// ConfirmTOTP passed its session check (the sign-out is not committed)
	// and confirmed the secret, and now waits for the session row.
	waitForLockWait(lockWaitCtx(t), t, backendPID(ctx, t, tx))
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("ConfirmTOTP on a session signed out midway = %v, want ErrNotFound", err)
	}
	confirmed, codes := confirmedAndCodes(t, w.q, w.d, uid)
	if confirmed || codes != 0 {
		t.Fatalf("after the rolled-back confirmation: confirmed=%v, recovery codes=%d", confirmed, codes)
	}
}

// backendPID is the server process behind q, the transaction the other
// side is meant to wait for (waitForLockWait, last_sign_in_sql_test.go).
func backendPID(ctx context.Context, t *testing.T, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) int {
	t.Helper()
	var pid int
	if err := q.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

// lockWaitCtx bounds how long a test waits for the other transaction to
// queue behind the lock.
func lockWaitCtx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// Two enrolments on one pending account must not deadlock each other
// (backlog 7.109, fix round 1). Each takes the account row before it
// writes, and completing a pending session updates that row
// (last_sign_in_at); a lock on it that conflicted with that update made two
// concurrent finishes wait on each other. Both tests hold the enrolments at
// the same point with a blocking transaction, release them together, and
// assert the outcome rather than which one Postgres picked as a victim.
//
// A TOTP confirmation submitted twice on the same pending session: the
// first commits; the second then finds the code's step already spent and is
// refused as an invalid code — no deadlock, and the account carries exactly
// the one set of recovery codes the first response returned.
func TestConcurrentTOTPConfirmsDoNotDeadlock(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "totp-double@example.test")
	p := w.session(t, uid, false)
	code := enrol(t, w.totp, w.clock, uid)

	// Hold the TOTP row, so both confirmations take the account row and
	// then queue on it.
	tx, err := w.d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM totp_secrets WHERE user_id = $1 FOR UPDATE`, uid); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, _, err := w.enrol.ConfirmTOTP(context.Background(), uid, p.SessionID, code)
			results <- err
		}()
	}
	waitForLockWaiters(lockWaitCtx(t), t, 2)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var ok int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, auth.ErrInvalidTOTPCode):
			t.Errorf("the second confirmation = %v, want ErrInvalidTOTPCode (its code was spent), never a deadlock", err)
		}
	}
	if ok != 1 {
		t.Fatalf("successful confirmations = %d, want exactly 1", ok)
	}
	confirmed, codes := confirmedAndCodes(t, w.q, w.d, uid)
	if !confirmed || codes != 8 {
		t.Fatalf("after the confirmations: confirmed=%v, recovery codes=%d, want true and 8", confirmed, codes)
	}
}

// Two pending sessions of one account each finishing a passkey: both
// complete, one after the other — two passkeys, two complete sessions, no
// deadlock.
func TestConcurrentPasskeyFinishesDoNotDeadlock(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "passkey-double@example.test")
	type finish struct {
		p        auth.Principal
		cid      uuid.UUID
		response []byte
	}
	var finishes []finish
	for range 2 {
		p := w.session(t, uid, false)
		opts, cid, err := w.passkeys.BeginRegistration(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		finishes = append(finishes, finish{p: p, cid: cid,
			response: buildNoneAttestationResponse(t, opts, testRPID, testOrigin, randomCredentialID(t))})
	}

	// Hold both session rows, so both finishes take the account row, store
	// their passkey and then queue on their session's upgrade.
	tx, err := w.d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM sessions WHERE user_id = $1 FOR UPDATE`, uid); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, f := range finishes {
		go func() {
			_, err := w.enrol.FinishPasskey(context.Background(), f.p, f.cid, "Key", f.response)
			results <- err
		}()
	}
	waitForLockWaiters(lockWaitCtx(t), t, 2)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("FinishPasskey = %v, want both to succeed, never a deadlock", err)
		}
	}
	if n := w.passkeyCount(t, uid); n != 2 {
		t.Fatalf("passkeys = %d, want 2", n)
	}
	var pending int
	if err := w.d.Pool().QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1 AND NOT mfa_complete`, uid).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Errorf("sessions still pending = %d, want 0", pending)
	}
}

// waitForLockWaiters returns once n backends of the test's database wait
// for a lock, read as the bootstrap superuser like waitForLockWait
// (last_sign_in_sql_test.go), and fails the test when ctx ends first. It
// counts every waiter, not only those blocked by the holding transaction
// directly: a second transaction queued on the same row waits behind the
// first one in the queue.
func waitForLockWaiters(ctx context.Context, t *testing.T, n int) {
	t.Helper()
	conn, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	for {
		var waiting int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting >= n {
			return
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("only %d of %d transactions ever waited on the lock", waiting, n)
		}
	}
}
