package tenancy_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/tenancy"
)

func (f *fakeStore) RotateInvite(_ context.Context, orgID, id uuid.UUID, next tenancy.InviteRotation) (tenancy.Invite, tenancy.InviteRotation, error) {
	if err := f.take("RotateInvite"); err != nil {
		return tenancy.Invite{}, tenancy.InviteRotation{}, err
	}
	for k, inv := range f.invites {
		if inv.OrgID != orgID || inv.ID != id {
			continue
		}
		prev := tenancy.InviteRotation{TokenHash: []byte(k), ExpiresAt: inv.ExpiresAt, InvitedBy: inv.InvitedBy}
		delete(f.invites, k)
		inv.ExpiresAt, inv.InvitedBy = next.ExpiresAt, next.InvitedBy
		f.invites[string(next.TokenHash)] = inv
		return inv, prev, nil
	}
	return tenancy.Invite{}, tenancy.InviteRotation{}, tenancy.ErrNotFound
}

// RestoreInvite fails on a cancelled context, as a real query does.
func (f *fakeStore) RestoreInvite(ctx context.Context, orgID, id uuid.UUID, current []byte, prev tenancy.InviteRotation) error {
	if err := f.take("RestoreInvite"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	inv, ok := f.invites[string(current)]
	if !ok || inv.OrgID != orgID || inv.ID != id {
		return nil
	}
	delete(f.invites, string(current))
	inv.ExpiresAt, inv.InvitedBy = prev.ExpiresAt, prev.InvitedBy
	f.invites[string(prev.TokenHash)] = inv
	return nil
}

// resendFixture is an organization with an owner, a second admin and one
// mailed invitation the owner sent, on a clock the test moves.
type resendFixture struct {
	st    *fakeStore
	mail  *fakeMailer
	svc   *tenancy.Service
	now   *time.Time
	org   uuid.UUID
	owner uuid.UUID
	admin uuid.UUID
	inv   tenancy.Invite
}

func newResendFixture(t *testing.T) resendFixture {
	t.Helper()
	ctx := context.Background()
	st := newFakeStore()
	m := &fakeMailer{}
	now := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	svc := tenancy.NewService(st, m, testPublicURL, func() time.Time { return now })
	owner := st.addUser("owner@x", true)
	org, err := svc.CreateOrg(ctx, owner, "acme", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	admin := st.addUser("admin@x", true)
	st.addMember(org.ID, admin, tenancy.RoleAdmin)
	inv, err := svc.Invite(ctx, org.ID, owner, "new@x", tenancy.RoleOperator)
	if err != nil {
		t.Fatal(err)
	}
	return resendFixture{st: st, mail: m, svc: svc, now: &now, org: org.ID, owner: owner, admin: admin, inv: inv}
}

// TestResendInviteRotatesTheLink is spec 2026-11-19 §4: a resend mails a
// fresh link, the old one stops working, the expiry starts over and the
// resender becomes the invitation's issuer.
func TestResendInviteRotatesTheLink(t *testing.T) {
	ctx := context.Background()
	f := newResendFixture(t)
	*f.now = f.now.Add(3 * 24 * time.Hour)

	got, err := f.svc.ResendInvite(ctx, f.org, f.admin, f.inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != f.inv.ID || got.Email != "new@x" || got.Role != tenancy.RoleOperator || !got.Mailed || got.AcceptURL != "" {
		t.Fatalf("resent invite = %+v", got)
	}
	if got.Token == "" || got.Token == f.inv.Token {
		t.Fatalf("token was not rotated: %q", got.Token)
	}
	if want := f.now.Add(tenancy.InviteTTL); !got.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, want)
	}
	if len(f.mail.sent) != 2 || f.mail.sent[1].to != "new@x" || !strings.Contains(f.mail.sent[1].body, testPublicURL+"/invite#token="+got.Token) {
		t.Fatalf("resend mail = %+v", f.mail.sent)
	}
	pending, err := f.svc.PendingInvites(ctx, f.org, false)
	if err != nil || len(pending) != 1 || pending[0].InvitedBy != f.admin {
		t.Fatalf("pending = %+v, %v; want one invitation issued by the resender", pending, err)
	}

	if _, _, _, err := f.svc.AcceptInvite(ctx, uuid.Nil, f.inv.Token, "Old Link", "correct horse battery"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("old token after resend: %v, want ErrNotFound", err)
	}
	if _, orgID, _, err := f.svc.AcceptInvite(ctx, uuid.Nil, got.Token, "New Link", "correct horse battery"); err != nil || orgID != f.org {
		t.Fatalf("new token: %v", err)
	}
}

// TestResendInviteRevivesAnExpiredOne: an expired invitation can be sent
// again, and its new link works.
func TestResendInviteRevivesAnExpiredOne(t *testing.T) {
	ctx := context.Background()
	f := newResendFixture(t)
	*f.now = f.now.Add(tenancy.InviteTTL + time.Hour)
	if _, _, _, err := f.svc.AcceptInvite(ctx, uuid.Nil, f.inv.Token, "Late", "correct horse battery"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("expired token: %v, want ErrNotFound", err)
	}

	got, err := f.svc.ResendInvite(ctx, f.org, f.owner, f.inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.svc.AcceptInvite(ctx, uuid.Nil, got.Token, "On Time", "correct horse battery"); err != nil {
		t.Fatalf("resent token: %v", err)
	}
}

// TestResendInviteNotFound: an unknown id, another organization's
// invitation and an accepted one all answer ErrNotFound, and nothing is
// mailed.
func TestResendInviteNotFound(t *testing.T) {
	ctx := context.Background()
	f := newResendFixture(t)
	other, err := f.svc.CreateOrg(ctx, f.owner, "other", "Other")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.svc.Invite(ctx, f.org, f.owner, "taken@x", tenancy.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.svc.AcceptInvite(ctx, uuid.Nil, accepted.Token, "Taken", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	mailed := len(f.mail.sent)

	for name, c := range map[string]struct{ org, id uuid.UUID }{
		"unknown":   {f.org, uuid.Must(uuid.NewV7())},
		"other org": {other.ID, f.inv.ID},
		"accepted":  {f.org, accepted.ID},
	} {
		if _, err := f.svc.ResendInvite(ctx, c.org, f.owner, c.id); !errors.Is(err, tenancy.ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
	if len(f.mail.sent) != mailed {
		t.Fatalf("a refused resend was mailed: %+v", f.mail.sent[mailed:])
	}
}

// TestResendInviteWithoutMailReturnsTheLink mirrors Invite: with no
// outgoing mail the new link comes back for the administrator to hand
// over.
func TestResendInviteWithoutMailReturnsTheLink(t *testing.T) {
	ctx := context.Background()
	f := newResendFixture(t)
	f.mail.failErr = fmt.Errorf("load smtp settings: %w", mail.ErrNotConfigured)

	got, err := f.svc.ResendInvite(ctx, f.org, f.owner, f.inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mailed || got.AcceptURL != testPublicURL+"/invite#token="+got.Token {
		t.Fatalf("resent invite = %+v, want the accept URL and mailed=false", got)
	}
}

// TestResendInviteDeliveryFailureChangesNothing: when the mail cannot be
// delivered the invitation keeps its old link, expiry and issuer, and the
// call reports ErrInviteDelivery.
func TestResendInviteDeliveryFailureChangesNothing(t *testing.T) {
	ctx := context.Background()
	f := newResendFixture(t)
	*f.now = f.now.Add(24 * time.Hour)
	f.mail.failErr = errors.New("550 mailbox unavailable")

	if _, err := f.svc.ResendInvite(ctx, f.org, f.admin, f.inv.ID); !errors.Is(err, tenancy.ErrInviteDelivery) {
		t.Fatalf("resend with a failing mailer: %v, want ErrInviteDelivery", err)
	}
	pending, err := f.svc.PendingInvites(ctx, f.org, false)
	if err != nil || len(pending) != 1 || pending[0].InvitedBy != f.owner || !pending[0].ExpiresAt.Equal(f.inv.ExpiresAt) {
		t.Fatalf("pending after a failed resend = %+v, %v; want the invitation as it was", pending, err)
	}
	if _, _, _, err := f.svc.AcceptInvite(ctx, uuid.Nil, f.inv.Token, "Old Link", "correct horse battery"); err != nil {
		t.Fatalf("old token after a failed resend: %v", err)
	}
}

// cancellingMailer stands for a client that disconnects while a slow SMTP
// failure is under way: the request is cancelled before the send fails.
type cancellingMailer struct{ cancel context.CancelFunc }

func (m cancellingMailer) Send(context.Context, string, string, string) error {
	m.cancel()
	return errors.New("451 try again later")
}

// TestResendInviteRestoresAfterTheRequestIsGone: the restore after a failed
// delivery does not depend on the request still being there, or the old
// link would be dead and the new one never delivered.
func TestResendInviteRestoresAfterTheRequestIsGone(t *testing.T) {
	f := newResendFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := tenancy.NewService(f.st, cancellingMailer{cancel: cancel}, testPublicURL, func() time.Time { return *f.now })

	if _, err := svc.ResendInvite(ctx, f.org, f.admin, f.inv.ID); !errors.Is(err, tenancy.ErrInviteDelivery) || strings.Contains(err.Error(), "restoring") {
		t.Fatalf("resend with a disconnecting client: %v, want ErrInviteDelivery with the invitation restored", err)
	}
	if _, _, _, err := f.svc.AcceptInvite(context.Background(), uuid.Nil, f.inv.Token, "Old Link", "correct horse battery"); err != nil {
		t.Fatalf("old token after a failed resend on a gone request: %v", err)
	}
}
