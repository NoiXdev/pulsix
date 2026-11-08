package accounts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
)

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
