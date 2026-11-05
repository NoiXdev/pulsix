package accounts_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/accounts"
)

var testNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func newAdminService(store *fakeStore, mailer *fakeMailer) *accounts.Service {
	return accounts.NewService(store, mailer, "https://pulsix.example", func() time.Time { return testNow }, rand.Reader)
}

// TestAccountStatePrecedence: the first matching state wins, in the order
// deactivated, invite pending, must change password, no second factor,
// active.
func TestAccountStatePrecedence(t *testing.T) {
	disabled := testNow.Add(-time.Hour)
	for _, tc := range []struct {
		name string
		rec  accounts.AccountRecord
		want accounts.AccountState
	}{
		{"active", accounts.AccountRecord{HasPassword: true, HasSecondFactor: true}, accounts.StateActive},
		{"deactivated", accounts.AccountRecord{HasPassword: true, HasSecondFactor: true, DisabledAt: &disabled}, accounts.StateDeactivated},
		{"deactivated and invite pending", accounts.AccountRecord{DisabledAt: &disabled}, accounts.StateDeactivated},
		{"invite pending", accounts.AccountRecord{}, accounts.StateInvitePending},
		{"invite pending wins over must change", accounts.AccountRecord{MustChangePassword: true}, accounts.StateInvitePending},
		{"must change password", accounts.AccountRecord{HasPassword: true, MustChangePassword: true}, accounts.StateMustChangePassword},
		{"must change wins over no second factor", accounts.AccountRecord{HasPassword: true, MustChangePassword: true, HasSecondFactor: false}, accounts.StateMustChangePassword},
		{"no second factor", accounts.AccountRecord{HasPassword: true}, accounts.StateNoSecondFactor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.rec.ID, tc.rec.Email = uuid.Must(uuid.NewV7()), "a@example.test"
			svc := newAdminService(&fakeStore{records: []accounts.AccountRecord{tc.rec}}, &fakeMailer{})
			users, _, err := svc.ListUsers(context.Background(), true, "", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(users) != 1 || users[0].State != tc.want {
				t.Fatalf("ListUsers = %+v, want one user in state %q", users, tc.want)
			}

			id := tc.rec.ID
			detail := accounts.AccountDetailRecord{AccountRecord: tc.rec}
			svc = newAdminService(&fakeStore{detail: &detail}, &fakeMailer{})
			got, err := svc.GetUser(context.Background(), true, uuid.Must(uuid.NewV7()), id)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.want {
				t.Fatalf("GetUser state %q, want %q", got.State, tc.want)
			}
		})
	}
}

func TestListUsersPaging(t *testing.T) {
	ctx := context.Background()
	var recs []accounts.AccountRecord
	for i := range 5 {
		recs = append(recs, accounts.AccountRecord{ID: uuid.Must(uuid.NewV7()), Email: fmt.Sprintf("user%d@example.test", i), HasPassword: true})
	}
	store := &fakeStore{records: recs}
	svc := newAdminService(store, &fakeMailer{})

	page, next, err := svc.ListUsers(ctx, true, "  needle  ", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Email != recs[0].Email || page[1].Email != recs[1].Email || next == "" {
		t.Fatalf("first page = %+v, next %q", page, next)
	}
	if got := store.listCalls[0]; got.query != "needle" || got.after != "" || got.limit != 3 {
		t.Fatalf("store asked with %+v, want the trimmed query, no lower bound and one row more than the page", got)
	}

	page, next, err = svc.ListUsers(ctx, true, "", next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Email != recs[2].Email || store.listCalls[1].after != recs[1].Email {
		t.Fatalf("second page = %+v after %q", page, store.listCalls[1].after)
	}
	page, next, err = svc.ListUsers(ctx, true, "", next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Email != recs[4].Email || next != "" {
		t.Fatalf("last page = %+v, next %q, want one user and no next cursor", page, next)
	}

	for _, tc := range []struct {
		limit, want int
	}{{0, 51}, {-3, 51}, {200, 201}, {201, 201}, {10_000, 201}} {
		if _, _, err := svc.ListUsers(ctx, true, "", "", tc.limit); err != nil {
			t.Fatal(err)
		}
		if got := store.listCalls[len(store.listCalls)-1].limit; got != tc.want {
			t.Errorf("limit %d asked the store for %d rows, want %d", tc.limit, got, tc.want)
		}
	}

	if _, _, err := svc.ListUsers(ctx, true, "", "not base64!", 2); !errors.Is(err, accounts.ErrInvalidCursor) {
		t.Fatalf("garbage cursor: err %v, want ErrInvalidCursor", err)
	}
	if _, _, err := svc.ListUsers(ctx, false, "", "", 2); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("not an admin: err %v, want ErrForbidden", err)
	}
}

func TestGetUser(t *testing.T) {
	ctx := context.Background()
	actor, id := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	expires := testNow.Add(time.Hour)
	detail := accounts.AccountDetailRecord{
		AccountRecord: accounts.AccountRecord{
			ID: id, Email: "detail@example.test", DisplayName: "Detail", InstanceAdmin: true,
			HasPassword: true, MustChangePassword: true, HasSecondFactor: true, OrgCount: 1,
		},
		CreatedAt: testNow.Add(-24 * time.Hour), TempPasswordExpiresAt: &expires,
		Factors:        accounts.Factors{TOTP: true, Passkeys: 2, RecoveryCodesLeft: 7},
		ActiveSessions: 3,
	}
	memberships := []accounts.Membership{{OrgID: uuid.Must(uuid.NewV7()), Slug: "acme", Name: "Acme", Role: "owner", ActorIsMember: true}}
	store := &fakeStore{detail: &detail, memberships: memberships}
	svc := newAdminService(store, &fakeMailer{})

	got, err := svc.GetUser(ctx, true, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Email != detail.Email || !got.InstanceAdmin || got.OrgCount != 1 || !got.MustChangePassword ||
		got.TempPasswordExpiresAt == nil || !got.TempPasswordExpiresAt.Equal(expires) || !got.CreatedAt.Equal(detail.CreatedAt) ||
		got.Factors != detail.Factors || got.ActiveSessions != 3 || got.State != accounts.StateMustChangePassword {
		t.Fatalf("GetUser = %+v", got)
	}
	if got.InvitePending {
		t.Fatalf("InvitePending = true for an account with a password")
	}
	if len(got.Memberships) != 1 || got.Memberships[0] != memberships[0] {
		t.Fatalf("memberships = %+v", got.Memberships)
	}
	if len(store.memberCalls) != 1 || store.memberCalls[0] != (memberCall{id, actor}) {
		t.Fatalf("ListMemberships calls = %+v, want one for the user, seen by the actor", store.memberCalls)
	}

	// A deactivated invitee shows as deactivated, and still says that its
	// invitation is pending: only reactivation is open to it.
	disabled := testNow
	invitee := accounts.AccountDetailRecord{AccountRecord: accounts.AccountRecord{ID: id, DisabledAt: &disabled}}
	got, err = newAdminService(&fakeStore{detail: &invitee}, &fakeMailer{}).GetUser(ctx, true, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != accounts.StateDeactivated || !got.InvitePending {
		t.Fatalf("deactivated invitee: state %q, InvitePending %v; want deactivated, true", got.State, got.InvitePending)
	}

	if _, err := svc.GetUser(ctx, false, actor, id); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("not an admin: err %v, want ErrForbidden", err)
	}
	if _, err := newAdminService(&fakeStore{}, &fakeMailer{}).GetUser(ctx, true, actor, id); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("unknown account: err %v, want ErrNotFound", err)
	}
}
