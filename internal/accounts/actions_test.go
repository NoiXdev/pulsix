package accounts_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/mail"
)

// cyclingReader yields 0, 1, ..., 255, 0, 1, ... forever: fixed, and it
// covers the byte values a uniform pick from the alphabet has to discard.
type cyclingReader struct{ next byte }

func (r *cyclingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.next
		r.next++
	}
	return len(p), nil
}

var temporaryPasswordShape = regexp.MustCompile(`^[a-hj-km-np-z2-9]{5}(-[a-hj-km-np-z2-9]{5}){3}$`)

func actionService(store *fakeStore, mailer *fakeMailer) *accounts.Service {
	return accounts.NewService(store, mailer, "https://pulsix.example", func() time.Time { return testNow }, &cyclingReader{})
}

func TestSetTemporaryPassword(t *testing.T) {
	ctx := context.Background()
	actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	newStore := func() *fakeStore {
		return &fakeStore{
			accounts:  map[uuid.UUID]bool{target: true},
			recipient: accounts.Recipient{Email: "target@example.test"},
		}
	}

	store, mailer := newStore(), &fakeMailer{}
	tmp, notice, err := actionService(store, mailer).SetTemporaryPassword(ctx, true, actor, target)
	if err != nil {
		t.Fatal(err)
	}
	if !temporaryPasswordShape.MatchString(tmp.Password) {
		t.Fatalf("temporary password %q does not have the shape xxxxx-xxxxx-xxxxx-xxxxx from the alphabet", tmp.Password)
	}
	if want := testNow.Add(24 * time.Hour); !tmp.ExpiresAt.Equal(want) {
		t.Fatalf("expires at %v, want %v", tmp.ExpiresAt, want)
	}
	if len(store.tempCalls) != 1 || store.tempCalls[0].userID != target || !store.tempCalls[0].expiresAt.Equal(tmp.ExpiresAt) {
		t.Fatalf("SetTemporaryPassword calls = %+v", store.tempCalls)
	}
	if ok, err := auth.VerifyPassword(store.tempCalls[0].hash, tmp.Password); err != nil || !ok {
		t.Fatalf("the stored hash does not verify the temporary password: ok=%v err=%v", ok, err)
	}
	if strings.Join(store.actions, ", ") != "set temporary password, revoke sessions, void reset tokens" || store.writesOutside != 0 {
		t.Fatalf("writes %v (%d outside the transaction), want the password, the revoke and the voided links inside it", store.actions, store.writesOutside)
	}
	if notice != accounts.NoticeSent || len(mailer.sent) != 1 || mailer.sent[0].to != "target@example.test" {
		t.Fatalf("notice %q, mails %+v, want one sent to the target", notice, mailer.sent)
	}
	if strings.Contains(mailer.sent[0].body, tmp.Password) || strings.Contains(mailer.sent[0].subject, tmp.Password) {
		t.Fatal("the notice mail carries the temporary password")
	}

	t.Run("a second password differs", func(t *testing.T) {
		svc := actionService(newStore(), &fakeMailer{})
		a, _, err := svc.SetTemporaryPassword(ctx, true, actor, target)
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := svc.SetTemporaryPassword(ctx, true, actor, target)
		if err != nil {
			t.Fatal(err)
		}
		if a.Password == b.Password {
			t.Fatalf("two temporary passwords are both %q", a.Password)
		}
	})

	t.Run("mail not configured", func(t *testing.T) {
		store, mailer := newStore(), &fakeMailer{unconfigured: true}
		_, notice, err := actionService(store, mailer).SetTemporaryPassword(ctx, true, actor, target)
		if err != nil {
			t.Fatal(err)
		}
		if notice != accounts.NoticeNotConfigured || len(mailer.sent) != 0 || len(store.tempCalls) != 1 {
			t.Fatalf("notice %q, mails %d, password set %d times", notice, len(mailer.sent), len(store.tempCalls))
		}
	})

	t.Run("mail fails", func(t *testing.T) {
		store, mailer := newStore(), &fakeMailer{sendErr: errors.New("smtp down")}
		_, notice, err := actionService(store, mailer).SetTemporaryPassword(ctx, true, actor, target)
		if err != nil {
			t.Fatalf("a failed notice failed the action: %v", err)
		}
		if notice != accounts.NoticeFailed || len(store.tempCalls) != 1 {
			t.Fatalf("notice %q, password set %d times, want failed and the password set", notice, len(store.tempCalls))
		}
	})

	t.Run("randomness runs out", func(t *testing.T) {
		store := newStore()
		svc := accounts.NewService(store, &fakeMailer{}, "https://pulsix.example", func() time.Time { return testNow }, bytes.NewReader(nil))
		if _, _, err := svc.SetTemporaryPassword(ctx, true, actor, target); err == nil {
			t.Fatal("no error without randomness")
		}
		if len(store.actions) != 0 {
			t.Fatalf("writes %v without a password", store.actions)
		}
	})
}

func TestResetSecondFactor(t *testing.T) {
	ctx := context.Background()
	actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := &fakeStore{accounts: map[uuid.UUID]bool{target: true}, recipient: accounts.Recipient{Email: "t@example.test"}}
	mailer := &fakeMailer{}
	notice, err := actionService(store, mailer).ResetSecondFactor(ctx, true, actor, target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(store.actions, ", ") != "delete second factors, revoke sessions, void reset tokens" || store.writesOutside != 0 {
		t.Fatalf("writes %v (%d outside the transaction)", store.actions, store.writesOutside)
	}
	if notice != accounts.NoticeSent || len(mailer.sent) != 1 {
		t.Fatalf("notice %q, mails %+v", notice, mailer.sent)
	}
}

// TestLockOutVoidsResetLinks: an administrator who sets a temporary
// password or resets the second factor suspects someone else has the
// account, maybe its mailbox too. A reset link issued before that must
// not undo the lock-out by setting a password of the holder's choosing.
func TestLockOutVoidsResetLinks(t *testing.T) {
	ctx := context.Background()
	actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for name, act := range map[string]func(svc *accounts.Service) error{
		"temporary password": func(svc *accounts.Service) error {
			_, _, err := svc.SetTemporaryPassword(ctx, true, actor, target)
			return err
		},
		"second factor reset": func(svc *accounts.Service) error {
			_, err := svc.ResetSecondFactor(ctx, true, actor, target)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			outstanding := &fakeToken{ResetToken: accounts.ResetToken{
				ID: uuid.Must(uuid.NewV7()), UserID: target, Hash: []byte("outstanding"),
				CreatedAt: testNow.Add(-time.Minute), ExpiresAt: testNow.Add(59 * time.Minute),
			}}
			store := &fakeStore{
				accounts:  map[uuid.UUID]bool{target: true},
				recipient: accounts.Recipient{Email: "target@example.test"},
				tokens:    []*fakeToken{outstanding},
			}
			if err := act(actionService(store, &fakeMailer{})); err != nil {
				t.Fatal(err)
			}
			if outstanding.usedAt == nil || store.writesOutside != 0 {
				t.Fatalf("the outstanding reset link is still usable (writes outside the transaction: %d)", store.writesOutside)
			}
		})
	}
}

func TestEndSessions(t *testing.T) {
	ctx := context.Background()
	actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store, mailer := &fakeStore{accounts: map[uuid.UUID]bool{target: true}}, &fakeMailer{}
	if err := actionService(store, mailer).EndSessions(ctx, true, actor, target); err != nil {
		t.Fatal(err)
	}
	if strings.Join(store.actions, ", ") != "revoke sessions" || len(mailer.sent) != 0 {
		t.Fatalf("writes %v, mails %d, want only the sessions revoked", store.actions, len(mailer.sent))
	}
}

func TestReactivate(t *testing.T) {
	ctx := context.Background()
	actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store, mailer := &fakeStore{accounts: map[uuid.UUID]bool{target: true}}, &fakeMailer{}
	if err := actionService(store, mailer).Reactivate(ctx, true, actor, target); err != nil {
		t.Fatal(err)
	}
	// Deactivation disables the account and revokes its sessions in two
	// steps, not one transaction, so reactivating ends any session that
	// slipped through, together with lifting the deactivation.
	// For the same reason a reset link issued before the deactivation is
	// voided.
	if strings.Join(store.actions, ", ") != "reactivate, revoke sessions, void reset tokens" || store.writesOutside != 0 || len(mailer.sent) != 0 {
		t.Fatalf("writes %v (%d outside), mails %d", store.actions, store.writesOutside, len(mailer.sent))
	}
}

// TestReactivateAnAccountWhoseInvitationIsPending is backlog 7.82 (5): an
// invitee deactivated before accepting can be reactivated. Nothing hands
// the account a password or a way to one — reactivation sets none — so the
// pending check other actions make does not apply; the invitation stays
// pending, and the account keeps no session and no reset link.
func TestReactivateAnAccountWhoseInvitationIsPending(t *testing.T) {
	ctx := context.Background()
	actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	store := &fakeStore{
		accounts:    map[uuid.UUID]bool{target: false},
		deactivated: map[uuid.UUID]bool{target: true},
	}
	mailer := &fakeMailer{}
	if err := actionService(store, mailer).Reactivate(ctx, true, actor, target); err != nil {
		t.Fatalf("reactivating a deactivated invitee: %v", err)
	}
	if strings.Join(store.actions, ", ") != "reactivate, revoke sessions, void reset tokens" || store.writesOutside != 0 || len(mailer.sent) != 0 {
		t.Fatalf("writes %v (%d outside), mails %d", store.actions, store.writesOutside, len(mailer.sent))
	}
	if len(store.setCalls) != 0 || len(store.tempCalls) != 0 {
		t.Fatalf("reactivation set a password: %v, %v", store.setCalls, store.tempCalls)
	}
}

// TestNoticesSkipDeactivatedAccounts: an action on a deactivated account
// still happens, but the person is not mailed about it (NoticeSkipped).
func TestNoticesSkipDeactivatedAccounts(t *testing.T) {
	ctx := context.Background()
	actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	newStore := func() *fakeStore {
		return &fakeStore{
			accounts:    map[uuid.UUID]bool{target: true},
			deactivated: map[uuid.UUID]bool{target: true},
			recipient:   accounts.Recipient{Email: "target@example.test"},
		}
	}

	store, mailer := newStore(), &fakeMailer{}
	_, notice, err := actionService(store, mailer).SetTemporaryPassword(ctx, true, actor, target)
	if err != nil {
		t.Fatal(err)
	}
	if notice != accounts.NoticeSkipped || len(mailer.sent) != 0 || len(store.tempCalls) != 1 {
		t.Fatalf("temporary password: notice %q, mails %d, password set %d times; want skipped, none, once", notice, len(mailer.sent), len(store.tempCalls))
	}

	store, mailer = newStore(), &fakeMailer{}
	notice, err = actionService(store, mailer).ResetSecondFactor(ctx, true, actor, target)
	if err != nil {
		t.Fatal(err)
	}
	if notice != accounts.NoticeSkipped || len(mailer.sent) != 0 || len(store.actions) == 0 {
		t.Fatalf("second factor: notice %q, mails %d, writes %v; want skipped, none, the reset done", notice, len(mailer.sent), store.actions)
	}
}

// adminActions are the account actions of this package, by name, each
// returning only its error.
func adminActions() map[string]func(svc *accounts.Service, isAdmin bool, actor, target uuid.UUID) error {
	ctx := context.Background()
	return map[string]func(*accounts.Service, bool, uuid.UUID, uuid.UUID) error{
		"set temporary password": func(svc *accounts.Service, isAdmin bool, actor, target uuid.UUID) error {
			_, _, err := svc.SetTemporaryPassword(ctx, isAdmin, actor, target)
			return err
		},
		"reset second factor": func(svc *accounts.Service, isAdmin bool, actor, target uuid.UUID) error {
			_, err := svc.ResetSecondFactor(ctx, isAdmin, actor, target)
			return err
		},
		"end sessions": func(svc *accounts.Service, isAdmin bool, actor, target uuid.UUID) error {
			return svc.EndSessions(ctx, isAdmin, actor, target)
		},
		"reactivate": func(svc *accounts.Service, isAdmin bool, actor, target uuid.UUID) error {
			return svc.Reactivate(ctx, isAdmin, actor, target)
		},
	}
}

func TestAdminActionsRefuseOwnAccount(t *testing.T) {
	for name, act := range adminActions() {
		t.Run(name, func(t *testing.T) {
			self := uuid.Must(uuid.NewV7())
			store, mailer := &fakeStore{accounts: map[uuid.UUID]bool{self: true}}, &fakeMailer{}
			if err := act(actionService(store, mailer), true, self, self); !errors.Is(err, accounts.ErrNotOnOwnAccount) {
				t.Fatalf("err %v, want ErrNotOnOwnAccount", err)
			}
			if len(store.actions) != 0 || len(mailer.sent) != 0 {
				t.Fatalf("a refused action wrote %v and sent %d mails", store.actions, len(mailer.sent))
			}
		})
	}
}

func TestAdminActionsRefuseInvitePending(t *testing.T) {
	for name, act := range adminActions() {
		if name == "reactivate" {
			// Allowed: TestReactivateAnAccountWhoseInvitationIsPending.
			continue
		}
		t.Run(name, func(t *testing.T) {
			actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			store, mailer := &fakeStore{accounts: map[uuid.UUID]bool{target: false}}, &fakeMailer{}
			if err := act(actionService(store, mailer), true, actor, target); !errors.Is(err, accounts.ErrInvitePending) {
				t.Fatalf("err %v, want ErrInvitePending", err)
			}
			if len(store.actions) != 0 || len(mailer.sent) != 0 {
				t.Fatalf("a refused action wrote %v and sent %d mails", store.actions, len(mailer.sent))
			}
		})
	}
}

func TestAdminActionsRefuseNonAdminsAndUnknownAccounts(t *testing.T) {
	for name, act := range adminActions() {
		t.Run(name, func(t *testing.T) {
			actor, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
			store := &fakeStore{accounts: map[uuid.UUID]bool{target: true}}
			if err := act(actionService(store, &fakeMailer{}), false, actor, target); !errors.Is(err, accounts.ErrForbidden) {
				t.Fatalf("not an admin: err %v, want ErrForbidden", err)
			}
			if err := act(actionService(store, &fakeMailer{}), true, actor, uuid.Must(uuid.NewV7())); !errors.Is(err, accounts.ErrNotFound) {
				t.Fatalf("unknown account: err %v, want ErrNotFound", err)
			}
			if len(store.actions) != 0 {
				t.Fatalf("a refused action wrote %v", store.actions)
			}
		})
	}
}

// TestNotifyDeactivated: the notice goes out in the person's effective
// language, resolved like /auth/me resolves it: their own choice, else the
// instance default, else English.
func TestNotifyDeactivated(t *testing.T) {
	de, en := "de", "en"
	for _, tc := range []struct {
		name      string
		recipient accounts.Recipient
		wantWord  string
	}{
		{"own choice", accounts.Recipient{Email: "a@example.test", Locale: &de, DefaultLocale: "en"}, "deaktiviert"},
		{"instance default", accounts.Recipient{Email: "a@example.test", DefaultLocale: "de"}, "deaktiviert"},
		{"own choice over the default", accounts.Recipient{Email: "a@example.test", Locale: &en, DefaultLocale: "de"}, "deactivated"},
		{"nothing set", accounts.Recipient{Email: "a@example.test"}, "deactivated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mailer := &fakeMailer{}
			notice := actionService(&fakeStore{recipient: tc.recipient}, mailer).NotifyDeactivated(context.Background(), uuid.Must(uuid.NewV7()), true)
			if notice != accounts.NoticeSent || len(mailer.sent) != 1 {
				t.Fatalf("notice %q, mails %+v", notice, mailer.sent)
			}
			if m := mailer.sent[0]; m.to != tc.recipient.Email || !strings.Contains(m.subject+m.body, tc.wantWord) {
				t.Fatalf("mail %+v, want it to %s with %q", m, tc.recipient.Email, tc.wantWord)
			}
		})
	}

	// An account that was already deactivated, or never claimed its
	// invitation, is told nothing.
	skipped := &fakeMailer{}
	if notice := actionService(&fakeStore{recipient: accounts.Recipient{Email: "a@example.test"}}, skipped).NotifyDeactivated(context.Background(), uuid.Must(uuid.NewV7()), false); notice != accounts.NoticeSkipped || len(skipped.sent) != 0 {
		t.Fatalf("not active before: notice %q, mails %d; want skipped and none", notice, len(skipped.sent))
	}

	if notice := actionService(&fakeStore{}, &fakeMailer{unconfigured: true}).NotifyDeactivated(context.Background(), uuid.Must(uuid.NewV7()), true); notice != accounts.NoticeNotConfigured {
		t.Fatalf("without mail: notice %q, want not_configured", notice)
	}
	// The settings went away between the check and the send.
	gone := &fakeMailer{sendErr: fmt.Errorf("load smtp settings: %w", mail.ErrNotConfigured)}
	if notice := actionService(&fakeStore{}, gone).NotifyDeactivated(context.Background(), uuid.Must(uuid.NewV7()), true); notice != accounts.NoticeNotConfigured {
		t.Fatalf("mail settings removed before the send: notice %q, want not_configured", notice)
	}
}
