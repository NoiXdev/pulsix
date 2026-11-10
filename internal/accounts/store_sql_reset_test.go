package accounts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// TestResetFlowSQL: a reset through the real store sets the password,
// lifts a forced change, uses up every token of the account and ends its
// sessions. While the change is pending, forgot sends no link; the
// administrator's does.
func TestResetFlowSQL(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	store := accounts.NewSQLStore(d)
	sessions := auth.NewSessions(gen.New(d.Pool()), time.Now)
	hash := "$argon2id$old"
	uid := seedUser(t, d, "flow@example.test", &hash)
	seedFactors(t, d, uid)
	if _, err := d.Pool().Exec(ctx,
		`UPDATE users SET must_change_password = true, temp_password_expires_at = now() + interval '1 hour' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	sid, _, err := sessions.Create(ctx, uid, true)
	if err != nil {
		t.Fatal(err)
	}
	mailer := &fakeMailer{}
	svc := accounts.NewService(store, mailer, "https://pulsix.example", time.Now, &cyclingReader{})

	if err := svc.RequestReset(ctx, "FLOW@example.test"); err != nil {
		t.Fatal(err)
	}
	svc.Wait()
	if len(mailer.sent) != 0 {
		t.Fatalf("forgot mailed %d links to an account that must change its password, want none", len(mailer.sent))
	}
	admin := uuid.Must(uuid.NewV7())
	for range 2 {
		if notice, err := svc.SendResetLink(ctx, true, admin, uid); err != nil || notice != accounts.NoticeSent {
			t.Fatalf("SendResetLink = %q, %v; want sent", notice, err)
		}
	}
	if len(mailer.sent) != 2 {
		t.Fatalf("mails %d, want 2", len(mailer.sent))
	}
	token := resetLink.FindStringSubmatch(mailer.sent[1].body)[1]
	if _, err := svc.ResetPassword(ctx, token, "a brand new password"); err != nil {
		t.Fatal(err)
	}

	var (
		got        string
		mustChange bool
		tempUntil  *time.Time
		unused     int
	)
	if err := d.Pool().QueryRow(ctx,
		`SELECT password_hash, must_change_password, temp_password_expires_at,
		        (SELECT count(*) FROM password_reset_tokens WHERE user_id = $1 AND used_at IS NULL)
		   FROM users WHERE id = $1`, uid).Scan(&got, &mustChange, &tempUntil, &unused); err != nil {
		t.Fatal(err)
	}
	if ok, err := auth.VerifyPassword(got, "a brand new password"); err != nil || !ok {
		t.Fatalf("the new password does not verify: %v %v", ok, err)
	}
	if mustChange || tempUntil != nil || unused != 0 {
		t.Fatalf("must change %v, temp until %v, unused tokens %d; want false, nil, 0", mustChange, tempUntil, unused)
	}
	if !sessionRevoked(t, d, sid) {
		t.Fatal("the session survived the reset")
	}
	if _, err := svc.ResetPassword(ctx, token, "another new password"); !errors.Is(err, accounts.ErrInvalidResetToken) {
		t.Fatalf("the token again: err %v, want ErrInvalidResetToken", err)
	}

	t.Run("a deactivated account's token", func(t *testing.T) {
		if err := svc.RequestReset(ctx, "flow@example.test"); err != nil {
			t.Fatal(err)
		}
		svc.Wait()
		token := resetLink.FindStringSubmatch(mailer.sent[len(mailer.sent)-1].body)[1]
		if _, err := d.Pool().Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, uid); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ResetPassword(ctx, token, "another new password"); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
	})
}

// TestResetConcurrentWithLinkRequestsSQL: resets and requests for a new
// link on one account, run at once, take their locks in the same order
// (the account, then its tokens), so none of them deadlocks; exactly one
// reset with a given token wins.
func TestResetConcurrentWithLinkRequestsSQL(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	hash := "$argon2id$old"
	uid := seedUser(t, d, "race@example.test", &hash)
	seedFactors(t, d, uid)
	mailer := &fakeMailer{}
	svc := accounts.NewService(accounts.NewSQLStore(d), mailer, "https://pulsix.example", time.Now, &cyclingReader{})
	if err := accounts.RequestResetNow(svc, ctx, "race@example.test"); err != nil {
		t.Fatal(err)
	}
	token := resetLink.FindStringSubmatch(mailer.sent[0].body)[1]
	other := uuid.Must(uuid.NewV7())

	const n = 4
	resetErrs, linkErrs := make(chan error, n), make(chan error, n)
	for range n {
		go func() {
			_, err := svc.ResetPassword(ctx, token, "a brand new password")
			resetErrs <- err
		}()
		go func() {
			_, err := svc.SendResetLink(ctx, true, other, uid)
			linkErrs <- err
		}()
	}
	won := 0
	for range n {
		switch err := <-resetErrs; {
		case err == nil:
			won++
		case errors.Is(err, accounts.ErrInvalidResetToken):
		default:
			t.Fatalf("concurrent reset: %v", err)
		}
		if err := <-linkErrs; err != nil {
			t.Fatalf("concurrent link request: %v", err)
		}
	}
	// A new link voids the raced one, so it may lose to them all; it never
	// wins twice.
	if won > 1 {
		t.Fatalf("%d resets with one token succeeded, want at most 1", won)
	}
}

// TestForgotWaitingBehindSecondFactorResetSendsNoLinkSQL: a forgot request
// that arrives while an administrator's second-factor reset holds the
// account's row waits for it, and once the reset has committed it must see
// the factors gone and issue no link (backlog 7.102). Transaction A here is
// that reset: LockAccount, then DeleteSecondFactors, held open until the
// request waits on the row. A second-factor check computed in LockAccount's
// own statement reads the factors from the snapshot taken before the wait,
// so it would still find them and mail a link.
func TestForgotWaitingBehindSecondFactorResetSendsNoLinkSQL(t *testing.T) {
	d := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hash := "$argon2id$old"
	uid := seedUser(t, d, "waiting@example.test", &hash)
	seedFactors(t, d, uid)
	mailer := &fakeMailer{}
	svc := accounts.NewService(accounts.NewSQLStore(d), mailer, "https://pulsix.example", time.Now, &cyclingReader{})

	txA, err := d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = txA.Rollback(context.Background()) }()
	var blockerPID int
	if err := txA.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	qA := gen.New(txA)
	if _, err := qA.LockAccount(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := qA.DeleteSecondFactors(ctx, uid); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- accounts.RequestResetNow(svc, ctx, "waiting@example.test") }()
	waitForLockWait(ctx, t, blockerPID)
	if err := txA.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the waiting forgot request: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the waiting forgot request never returned")
	}
	if n := countRows(t, d, "password_reset_tokens", uid); n != 0 {
		t.Errorf("%d reset tokens issued after the second-factor reset, want none", n)
	}
	if len(mailer.sent) != 0 {
		t.Errorf("forgot mailed %d links after the second-factor reset, want none", len(mailer.sent))
	}
}

// waitForLockWait returns once a backend of the test's database is blocked
// by the backend blockerPID (transaction A) and fails the test when ctx ends
// first. It reads pg_stat_activity as the bootstrap superuser (testdb.URL):
// the application role sees no other backend's wait event. A copy of the
// auth tests' helper.
func waitForLockWait(ctx context.Context, t *testing.T, blockerPID int) {
	t.Helper()
	conn, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			                 WHERE datname = current_database() AND wait_event_type = 'Lock'
			                   AND $1 = ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("the forgot request never waited on the account row")
		}
	}
}
