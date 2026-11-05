package accounts_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
)

func TestResetPassword(t *testing.T) {
	ctx := context.Background()
	const next = "a brand new password"

	t.Run("a valid token", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		f.store.actions = nil
		owner, err := f.svc.ResetPassword(ctx, token, next)
		if err != nil || owner != f.user {
			t.Fatalf("ResetPassword = %v, %v; want the user's account", owner, err)
		}
		if len(f.store.setCalls) != 1 || f.store.setCalls[0].userID != f.user || !f.store.setCalls[0].clearMustChange {
			t.Fatalf("SetPassword calls %+v, want one for the user that clears the forced change", f.store.setCalls)
		}
		if ok, err := auth.VerifyPassword(f.store.setCalls[0].hash, next); err != nil || !ok {
			t.Fatalf("the stored hash does not verify the new password: %v %v", ok, err)
		}
		if strings.Join(f.store.actions, ", ") != "void reset tokens, revoke sessions" || f.store.writesOutside != 0 {
			t.Fatalf("writes %v (%d outside the transaction), want the tokens voided and the sessions revoked inside it", f.store.actions, f.store.writesOutside)
		}
		if _, err := f.svc.ResetPassword(ctx, token, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("the same token again: err %v, want ErrInvalidResetToken", err)
		}
	})

	t.Run("an older token after a newer one was issued", func(t *testing.T) {
		f := newResetFixture()
		older := f.mailedToken(t)
		f.mailedToken(t)
		if _, err := f.svc.ResetPassword(ctx, older, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
	})

	t.Run("61 minutes later", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		*f.clock = testNow.Add(61 * time.Minute)
		if _, err := f.svc.ResetPassword(ctx, token, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
	})

	t.Run("a deactivated account", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		f.store.deactivated[f.user] = true
		if _, err := f.svc.ResetPassword(ctx, token, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
	})

	// A concurrent reset uses the token up between the unlocked owner
	// lookup and the lock: the locked re-check refuses it.
	t.Run("a token used in between", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		f.store.beforeTokenLock = func() {
			for _, tok := range f.store.tokens {
				used := testNow
				tok.usedAt = &used
			}
		}
		if _, err := f.svc.ResetPassword(ctx, token, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
		if len(f.store.setCalls) != 0 {
			t.Fatal("a password was stored with a used token")
		}
	})

	// The account goes between the unlocked owner lookup and the lock.
	t.Run("an account gone in between", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		f.store.beforeTokenLock = func() { delete(f.store.accounts, f.user) }
		if _, err := f.svc.ResetPassword(ctx, token, next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
	})

	t.Run("an unknown token", func(t *testing.T) {
		f := newResetFixture()
		if _, err := f.svc.ResetPassword(ctx, "not-a-token", next); !errors.Is(err, accounts.ErrInvalidResetToken) {
			t.Fatalf("err %v, want ErrInvalidResetToken", err)
		}
		if f.store.locks != 0 {
			t.Fatalf("an unknown token locked a row %d times", f.store.locks)
		}
	})

	t.Run("an 11-character password", func(t *testing.T) {
		f := newResetFixture()
		token := f.mailedToken(t)
		locks := f.store.locks
		if _, err := f.svc.ResetPassword(ctx, token, "elevenchars"); !errors.Is(err, auth.ErrPasswordTooShort) {
			t.Fatalf("err %v, want ErrPasswordTooShort", err)
		}
		if len(f.store.setCalls) != 0 {
			t.Fatal("a refused password was stored")
		}
		// The policy check and the hashing happen before the account's
		// row is locked.
		if f.store.locks != locks {
			t.Fatalf("a refused password locked the account's row %d times", f.store.locks-locks)
		}
		if _, err := f.svc.ResetPassword(ctx, token, next); err != nil {
			t.Fatalf("the token was consumed by the refused password: %v", err)
		}
	})

	if strings.Contains(accounts.ErrInvalidResetToken.Error(), "token=") {
		t.Fatal("the sentinel names a token")
	}
}

func TestSendResetLink(t *testing.T) {
	ctx := context.Background()
	actor := uuid.Must(uuid.NewV7())

	t.Run("mails a link without the per-address limit", func(t *testing.T) {
		f := newResetFixture()
		for i := range 3 {
			f.store.tokens = append(f.store.tokens, &fakeToken{ResetToken: accounts.ResetToken{
				ID: uuid.Must(uuid.NewV7()), UserID: f.user, Hash: []byte{byte(i)},
				CreatedAt: testNow.Add(-time.Minute), ExpiresAt: testNow.Add(59 * time.Minute),
			}})
		}
		notice, err := f.svc.SendResetLink(ctx, true, actor, f.user)
		if err != nil || notice != accounts.NoticeSent {
			t.Fatalf("notice %q, err %v; want sent", notice, err)
		}
		if len(f.mailer.sent) != 1 || !resetLink.MatchString(f.mailer.sent[0].body) || len(f.store.tokens) != 4 {
			t.Fatalf("mails %+v, token rows %d; want one link and a 4th row", f.mailer.sent, len(f.store.tokens))
		}
		for _, tok := range f.store.tokens[:3] {
			if tok.usedAt == nil {
				t.Fatal("an earlier token was not voided")
			}
		}
	})

	// The administrator's link is an explicit choice, so it still goes to
	// an account that must change its password, where forgot sends none.
	t.Run("an account that must change its password", func(t *testing.T) {
		f := newResetFixture()
		f.store.mustChange = map[uuid.UUID]bool{f.user: true}
		notice, err := f.svc.SendResetLink(ctx, true, actor, f.user)
		if err != nil || notice != accounts.NoticeSent || len(f.mailer.sent) != 1 {
			t.Fatalf("notice %q, err %v, mails %d; want one sent", notice, err, len(f.mailer.sent))
		}
	})

	t.Run("mail not configured", func(t *testing.T) {
		f := newResetFixture()
		f.mailer.unconfigured = true
		if _, err := f.svc.SendResetLink(ctx, true, actor, f.user); !errors.Is(err, accounts.ErrMailNotConfigured) {
			t.Fatalf("err %v, want ErrMailNotConfigured", err)
		}
		if len(f.store.tokens) != 0 || len(f.mailer.sent) != 0 {
			t.Fatal("a token or a mail without outbound mail")
		}
	})

	t.Run("a deactivated account gets nothing", func(t *testing.T) {
		f := newResetFixture()
		f.store.deactivated[f.user] = true
		notice, err := f.svc.SendResetLink(ctx, true, actor, f.user)
		if err != nil || notice != accounts.NoticeSkipped {
			t.Fatalf("notice %q, err %v; want skipped", notice, err)
		}
		if len(f.store.tokens) != 0 || len(f.mailer.sent) != 0 {
			t.Fatal("a deactivated account got a reset link")
		}
	})

	t.Run("a failed send", func(t *testing.T) {
		f := newResetFixture()
		logs := captureLog(t)
		f.mailer.sendErr = errors.New("smtp down")
		notice, err := f.svc.SendResetLink(ctx, true, actor, f.user)
		if err != nil || notice != accounts.NoticeFailed {
			t.Fatalf("notice %q, err %v; want failed", notice, err)
		}
		if len(f.store.tokens) != 1 || !strings.Contains(logs.String(), "smtp down") || strings.Contains(logs.String(), "token=") {
			t.Fatalf("token rows %d, log %s; want the token issued and the failure logged without the link", len(f.store.tokens), logs)
		}
	})

	t.Run("a browser that went away", func(t *testing.T) {
		f := newResetFixture()
		f.mailer.cancelAware = true
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if notice, err := f.svc.SendResetLink(cancelled, true, actor, f.user); err != nil || notice != accounts.NoticeSent {
			t.Fatalf("notice %q, err %v; want the send to go ahead", notice, err)
		}
	})

	for _, tc := range []struct {
		name    string
		isAdmin bool
		actor   func(f *resetFixture) uuid.UUID
		target  func(f *resetFixture) uuid.UUID
		prepare func(f *resetFixture)
		want    error
	}{
		{"not an admin", false, func(*resetFixture) uuid.UUID { return actor }, func(f *resetFixture) uuid.UUID { return f.user }, nil, accounts.ErrForbidden},
		{"own account", true, func(f *resetFixture) uuid.UUID { return f.user }, func(f *resetFixture) uuid.UUID { return f.user }, nil, accounts.ErrNotOnOwnAccount},
		{"unknown account", true, func(*resetFixture) uuid.UUID { return actor }, func(*resetFixture) uuid.UUID { return uuid.Must(uuid.NewV7()) }, nil, accounts.ErrNotFound},
		{"invite pending", true, func(*resetFixture) uuid.UUID { return actor }, func(f *resetFixture) uuid.UUID { return f.user }, func(f *resetFixture) { f.store.accounts[f.user] = false }, accounts.ErrInvitePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResetFixture()
			if tc.prepare != nil {
				tc.prepare(f)
			}
			if _, err := f.svc.SendResetLink(ctx, tc.isAdmin, tc.actor(f), tc.target(f)); !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
			if len(f.store.tokens) != 0 || len(f.mailer.sent) != 0 {
				t.Fatal("a refused call issued a token or sent a mail")
			}
		})
	}
}
