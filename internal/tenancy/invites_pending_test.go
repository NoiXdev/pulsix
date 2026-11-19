package tenancy_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestPendingInvitesIncludeExpiredOnRequest is the list half of spec
// 2026-11-19 §4: by default an expired invitation is not listed; asked
// for, it is, marked expired against the service's clock, so it can be
// resent.
func TestPendingInvitesIncludeExpiredOnRequest(t *testing.T) {
	ctx := context.Background()
	f := newResendFixture(t)
	*f.now = f.now.Add(tenancy.InviteTTL + time.Minute)
	fresh, err := f.svc.Invite(ctx, f.org, f.owner, "fresh@x", tenancy.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}

	open, err := f.svc.PendingInvites(ctx, f.org, false)
	if err != nil || len(open) != 1 || open[0].ID != fresh.ID || open[0].Expired {
		t.Fatalf("default list = %+v, %v; want only the fresh invitation, not expired", open, err)
	}
	all, err := f.svc.PendingInvites(ctx, f.org, true)
	if err != nil || len(all) != 2 {
		t.Fatalf("list with expired = %+v, %v; want both", all, err)
	}
	for _, inv := range all {
		if want := inv.ID == f.inv.ID; inv.Expired != want {
			t.Fatalf("%s: expired = %v, want %v", inv.Email, inv.Expired, want)
		}
	}
}

// TestPendingInvitesListsOnlyRecentlyExpired: asked for the expired ones,
// the list holds those that expired within ExpiredInviteListWindow of the
// service's clock; older ones stay out, so the list does not grow forever.
func TestPendingInvitesListsOnlyRecentlyExpired(t *testing.T) {
	ctx := context.Background()
	f := newResendFixture(t) // f.inv expires InviteTTL after the fixture's start
	expiry := f.inv.ExpiresAt

	cases := []struct {
		name   string
		now    time.Time
		listed bool
	}{
		{"just expired", expiry.Add(time.Minute), true},
		{"expired a day inside the window", expiry.Add(tenancy.ExpiredInviteListWindow - 24*time.Hour), true},
		{"expired a day beyond the window", expiry.Add(tenancy.ExpiredInviteListWindow + 24*time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			*f.now = tc.now
			all, err := f.svc.PendingInvites(ctx, f.org, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(all) == 1 && all[0].ID == f.inv.ID && all[0].Expired; got != tc.listed {
				t.Fatalf("list with expired = %+v, want listed %v", all, tc.listed)
			}
		})
	}
}
