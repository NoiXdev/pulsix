package accounts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

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
