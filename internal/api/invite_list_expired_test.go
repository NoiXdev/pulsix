package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestInviteListIncludesExpiredOnRequest is spec 2026-11-19 §4's list
// half: by default the list is as before, open invitations only;
// include_expired=true adds the ones that expired within the last 30 days,
// marked expired, so they can be resent; older ones stay out. Accepted invitations never come back, and the bar is
// members.manage as before.
func TestInviteListIncludesExpiredOnRequest(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("list-expired-owner@example.test", "correct horse battery")
	org := s.SeedOrg("list-expired-org", owner)
	operator := s.SeedUser("list-expired-operator@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/list-expired-org/invites"

	expiredID, _ := inviteFor(t, s, c, base, "expired-listed@example.test")
	if _, err := s.DB.Pool().Exec(context.Background(), "UPDATE invites SET expires_at = now() - interval '1 day' WHERE id = $1", expiredID); err != nil {
		t.Fatal(err)
	}
	longExpiredID, _ := inviteFor(t, s, c, base, "long-expired-listed@example.test")
	if _, err := s.DB.Pool().Exec(context.Background(), "UPDATE invites SET expires_at = now() - interval '31 days' WHERE id = $1", longExpiredID); err != nil {
		t.Fatal(err)
	}
	openID, _ := inviteFor(t, s, c, base, "open-listed@example.test")
	_, acceptedToken := inviteFor(t, s, c, base, "accepted-listed@example.test")
	if code := acceptInvite(t, s, acceptedToken); code != http.StatusOK {
		t.Fatalf("accept: status %d", code)
	}

	for _, url := range []string{base, base + "?include_expired=false"} {
		if got := listInvites(t, c, url); len(got) != 1 || got[0].ID != openID || got[0].Expired {
			t.Fatalf("%s = %+v, want only the open invitation", url, got)
		}
	}
	got := listInvites(t, c, base+"?include_expired=true")
	if len(got) != 2 {
		t.Fatalf("include_expired=true = %+v, want the open and the recently expired invitation", got)
	}
	for _, inv := range got {
		if want := inv.ID == expiredID; inv.Expired != want || (inv.ID != expiredID && inv.ID != openID) {
			t.Fatalf("include_expired=true listed %+v", inv)
		}
	}

	if resp := requestJSON(t, s.LoginAs(operator), http.MethodGet, base+"?include_expired=true", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator list with expired: status %d, want 403", resp.StatusCode)
	}
}
