// The owner removes a second factor (deletes a passkey, disables TOTP) in an
// instance transaction that takes the account row FOR UPDATE first (backlog
// 7.116). That lock conflicts with the key share a passkey login takes
// before it re-reads its credential and opens a session, so the two
// serialise: a login queued behind a deletion finds its passkey gone, and a
// deletion queued behind a login waits for it. It also serialises two
// removals of the account's last two factors, so the last-factor check of
// the second sees what the first removed. These tests drive the real
// database, as enrolment_reset_sql_test.go does.
package auth_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/auth"
)

// passkeyID is the id of uid's passkey whose credential id is credID.
func (w *resetWorld) passkeyID(t *testing.T, uid uuid.UUID, credID []byte) uuid.UUID {
	t.Helper()
	creds, err := w.passkeys.List(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range creds {
		if bytes.Equal(c.CredentialID, credID) {
			return c.ID
		}
	}
	t.Fatalf("uid %s has no passkey with that credential id", uid)
	return uuid.Nil
}

func (w *resetWorld) secondFactors() auth.SecondFactors {
	return auth.SecondFactors{TOTP: w.totp, Passkeys: w.passkeys}
}

// A login whose assertion was validated against passkey A, and which then
// queues behind the owner's deletion of A, opens no session: it waits for
// the deletion's exclusive lock and re-reads its credential after the
// deletion committed.
func TestPasskeyLoginWaitingBehindPasskeyDeletionOpensNoSession(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "login-behind-delete@example.test")
	a := w.seedLoginKey(t, uid)
	w.seedLoginKey(t, uid)
	idA := w.passkeyID(t, uid, a.credID)
	cid, response := beginLogin(t, w.passkeys, uid, a)
	before := w.sessionCount(t, uid)

	type result struct {
		cookie string
		err    error
	}
	finished := make(chan result, 1)
	err := w.d.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := w.sessions.LockAccountForRemoval(ctx, uid); err != nil {
			return err
		}
		go func() {
			cookie, err := w.passkeys.FinishLogin(context.Background(), cid, response)
			finished <- result{cookie, err}
		}()
		waitForLockWait(lockWaitCtx(t), t, backendPID(ctx, t, w.d))
		select {
		case r := <-finished:
			t.Fatalf("the login finished while a deletion held the account: %v", r.err)
		default:
		}
		return w.secondFactors().DeletePasskey(ctx, uid, idA)
	})
	if err != nil {
		t.Fatalf("delete passkey A: %v", err)
	}

	r := <-finished
	if !errors.Is(r.err, auth.ErrInvalidCredentials) {
		t.Fatalf("FinishLogin behind the deletion = %v, want ErrInvalidCredentials", r.err)
	}
	if r.cookie != "" {
		t.Error("a refused login handed out a session cookie")
	}
	if n := w.sessionCount(t, uid); n != before {
		t.Fatalf("sessions of the account = %d, want %d (unchanged)", n, before)
	}
	if n := w.passkeyCount(t, uid); n != 1 {
		t.Fatalf("passkeys after the deletion = %d, want 1", n)
	}
}

// The other order: a login that holds its key share on the account makes
// Enrolment.DeletePasskey wait, so the deletion's lock really is exclusive —
// a key-share lock would not conflict with the login's, and the deletion
// would run alongside it. The login opens its session (it came first), and
// the deletion then removes the passkey.
func TestPasskeyDeletionWaitsForALoginHoldingTheAccount(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "delete-behind-login@example.test")
	a := w.seedLoginKey(t, uid)
	w.seedLoginKey(t, uid)
	idA := w.passkeyID(t, uid, a.credID)

	deleted := make(chan error, 1)
	held := false
	sessions := &hookedSessions{Sessions: w.sessions}
	sessions.afterLock = func(ctx context.Context) {
		held = true
		go func() { deleted <- w.enrol.DeletePasskey(context.Background(), uid, idA) }()
		waitForLockWait(lockWaitCtx(t), t, backendPID(ctx, t, w.d))
		select {
		case err := <-deleted:
			t.Errorf("the deletion finished while a login held the account: %v", err)
		default:
		}
	}
	pk := w.loginPasskeys(t, sessions)
	cid, response := beginLogin(t, pk, uid, a)
	cookie, err := pk.FinishLogin(ctx, cid, response)
	if err != nil {
		t.Fatalf("FinishLogin while the deletion waited: %v", err)
	}
	if !held {
		t.Fatal("the login opened its session without taking the account lock")
	}
	if err := <-deleted; err != nil {
		t.Fatalf("Enrolment.DeletePasskey: %v", err)
	}
	if _, err := w.sessions.Lookup(ctx, cookie); err != nil {
		t.Errorf("the login's session: Lookup = %v, want it open", err)
	}
	if n := w.passkeyCount(t, uid); n != 1 {
		t.Fatalf("passkeys after the deletion = %d, want 1", n)
	}
}

// Deleting the account's only passkey and disabling its TOTP at once: each
// is allowed on its own, since the other factor remains, but together they
// would leave the account with none. Both queue behind a held account lock
// and run one after the other once it is released; whichever runs second
// sees the first's removal and is refused, and the account keeps a factor.
func TestConcurrentRemovalOfTheLastTwoFactorsKeepsOne(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "last-two@example.test")
	if _, err := w.totp.ConfirmEnrol(ctx, uid, enrol(t, w.totp, w.clock, uid)); err != nil {
		t.Fatal(err)
	}
	k := w.seedLoginKey(t, uid)
	id := w.passkeyID(t, uid, k.credID)

	results := make(chan error, 2)
	err := w.d.WithInstanceTx(ctx, func(ctx context.Context) error {
		if err := w.sessions.LockAccountForRemoval(ctx, uid); err != nil {
			return err
		}
		go func() { results <- w.enrol.DeletePasskey(context.Background(), uid, id) }()
		go func() { results <- w.enrol.DisableTOTP(context.Background(), uid) }()
		waitForLockWaiters(lockWaitCtx(t), t, 2)
		select {
		case err := <-results:
			t.Fatalf("a removal finished while the account was held: %v", err)
		default:
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var ok, refused int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			ok++
		case errors.Is(err, auth.ErrLastSecondFactor):
			refused++
		default:
			t.Errorf("a removal = %v, want nil or ErrLastSecondFactor", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("removals: %d succeeded, %d refused as the last factor; want 1 and 1", ok, refused)
	}
	enrolled, err := w.secondFactors().Enrolled(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if !enrolled {
		t.Fatal("the account was left with no second factor")
	}
}

// Without contention the two removals behave as SecondFactors' do: an
// unknown account's removal is ErrNotFound, a passkey another account owns
// is ErrNotFound, and the last factor is refused.
func TestFactorRemovalOutcomes(t *testing.T) {
	w := newResetWorld(t)
	uid := w.seedAccount(t, "removal-outcomes@example.test")
	k := w.seedLoginKey(t, uid)
	id := w.passkeyID(t, uid, k.credID)
	other := w.seedAccount(t, "removal-other@example.test")

	if err := w.enrol.DeletePasskey(ctx, uuid.Must(uuid.NewV7()), id); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("DeletePasskey on an unknown account = %v, want ErrNotFound", err)
	}
	if err := w.enrol.DisableTOTP(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("DisableTOTP on an unknown account = %v, want ErrNotFound", err)
	}
	if err := w.enrol.DeletePasskey(ctx, other, id); !errors.Is(err, auth.ErrNotFound) {
		t.Errorf("DeletePasskey of another account's passkey = %v, want ErrNotFound", err)
	}
	if err := w.enrol.DeletePasskey(ctx, uid, id); !errors.Is(err, auth.ErrLastSecondFactor) {
		t.Errorf("DeletePasskey of the last factor = %v, want ErrLastSecondFactor", err)
	}
	if err := w.enrol.DisableTOTP(ctx, other); !errors.Is(err, auth.ErrLastSecondFactor) {
		t.Errorf("DisableTOTP with no passkey to fall back on = %v, want ErrLastSecondFactor", err)
	}
	if n := w.passkeyCount(t, uid); n != 1 {
		t.Fatalf("passkeys after the refusals = %d, want 1", n)
	}
}
