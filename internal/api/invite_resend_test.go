package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/mail"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type inviteResent struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
	MailSent  bool      `json:"mail_sent"`
	AcceptURL string    `json:"accept_url"`
}

// inviteFor creates an invitation for email through the API and returns
// its id and the token its mail carried.
func inviteFor(t *testing.T, s *apitest.Server, c *http.Client, base, email string) (string, string) {
	t.Helper()
	if resp := postJSON(t, c, base, map[string]any{"email": email, "role": "operator"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	token := extractInviteToken(t, s)
	for _, inv := range listInvites(t, c, base) {
		if inv.Email == email {
			return inv.ID, token
		}
	}
	t.Fatalf("invitation for %s not listed", email)
	return "", ""
}

func acceptInvite(t *testing.T, s *apitest.Server, token string) int {
	t.Helper()
	resp := postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "Invitee", "password": "correct horse battery",
	})
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestResendInviteRotatesTheLink is spec 2026-11-19 §4 over HTTP: the
// resend mails a fresh link, the old one stops working, the new one admits
// the invitee, the expiry starts over, and the resend is audited.
func TestResendInviteRotatesTheLink(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("resend-owner@example.test", "correct horse battery")
	s.SeedOrg("resend-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/resend-org/invites"
	id, oldToken := inviteFor(t, s, c, base, "resend@example.test")
	before := listInvites(t, c, base)[0].ExpiresAt

	time.Sleep(10 * time.Millisecond) // a visibly later expiry
	resp := postJSON(t, c, base+"/"+id+"/resend", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resend: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var got inviteResent
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Email != "resend@example.test" || got.Role != "operator" || !got.MailSent || got.AcceptURL != "" {
		t.Fatalf("resend body = %+v", got)
	}
	if prev, err := time.Parse(time.RFC3339Nano, before); err != nil || !got.ExpiresAt.After(prev) {
		t.Fatalf("expires_at %v not after the original %s (%v)", got.ExpiresAt, before, err)
	}
	if sent := s.Mail.Sent(); len(sent) != 2 || sent[1].To != "resend@example.test" {
		t.Fatalf("mails = %+v, want the invitation and its resend", sent)
	}
	newToken := extractInviteToken(t, s)
	if newToken == oldToken {
		t.Fatal("the resend mailed the same link")
	}
	if code := acceptInvite(t, s, oldToken); code != http.StatusNotFound {
		t.Fatalf("old link after resend: status %d, want 404", code)
	}
	if code := acceptInvite(t, s, newToken); code != http.StatusOK {
		t.Fatalf("new link: status %d, want 200", code)
	}

	// Accepted is history: resending it is a 404, and so is an id that
	// is not an invitation at all.
	for _, bad := range []string{id, uuid.Must(uuid.NewV7()).String(), "not-a-uuid"} {
		if resp := postJSON(t, c, base+"/"+bad+"/resend", nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("resend %s: status %d, want 404", bad, resp.StatusCode)
		}
	}

	activity := decodeActivity(t, requestJSON(t, c, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/resend-org/activity?limit=50", nil))
	resent := 0
	for _, ev := range activity.Items {
		if ev.Action == "POST resendInvite" {
			resent++
			if ev.ObjectType != "invite" || ev.ObjectID != id || !strings.Contains(ev.Summary, "resend@example.test") {
				t.Fatalf("resend event = %+v", ev)
			}
		}
	}
	if resent != 1 {
		t.Fatalf("%d resend events, want 1: %+v", resent, activity.Items)
	}
}

// TestResendInviteRevivesAnExpiredOne: an expired invitation is no longer
// listed and its link is dead, but it can be resent, and the new link
// works.
func TestResendInviteRevivesAnExpiredOne(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("resend-expired-owner@example.test", "correct horse battery")
	s.SeedOrg("resend-expired-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/resend-expired-org/invites"
	id, oldToken := inviteFor(t, s, c, base, "expired@example.test")
	if _, err := s.DB.Pool().Exec(context.Background(), "UPDATE invites SET expires_at = now() - interval '1 day' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	if code := acceptInvite(t, s, oldToken); code != http.StatusNotFound {
		t.Fatalf("expired link: status %d, want 404", code)
	}

	if resp := postJSON(t, c, base+"/"+id+"/resend", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("resend expired: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if code := acceptInvite(t, s, extractInviteToken(t, s)); code != http.StatusOK {
		t.Fatalf("resent link: status %d, want 200", code)
	}
}

// TestResendRevokedInviteIsNotFound: a revoked invitation is gone.
func TestResendRevokedInviteIsNotFound(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("resend-revoked-owner@example.test", "correct horse battery")
	s.SeedOrg("resend-revoked-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/resend-revoked-org/invites"
	id, _ := inviteFor(t, s, c, base, "revoked@example.test")
	if resp := requestJSON(t, c, http.MethodDelete, base+"/"+id, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: status %d", resp.StatusCode)
	}
	if resp := postJSON(t, c, base+"/"+id+"/resend", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("resend revoked: status %d, want 404", resp.StatusCode)
	}
}

// TestResendInviteWithoutMailReturnsTheLink: with no outgoing mail the new
// link comes back, as on create.
func TestResendInviteWithoutMailReturnsTheLink(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("resend-nomail-owner@example.test", "correct horse battery")
	s.SeedOrg("resend-nomail-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/resend-nomail-org/invites"
	id, _ := inviteFor(t, s, c, base, "nomail-resend@example.test")
	s.Mail.FailWith(fmt.Errorf("load smtp settings: %w", mail.ErrNotConfigured))

	resp := postJSON(t, c, base+"/"+id+"/resend", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resend: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var got inviteResent
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.MailSent || !strings.HasPrefix(got.AcceptURL, s.HTTP.URL+"/invite#token=") {
		t.Fatalf("resend body = %+v, want mail_sent=false and an accept_url", got)
	}
}

// TestResendInviteDeliveryFailureIs502: a configured mailer that fails
// answers 502 and leaves the old link working.
func TestResendInviteDeliveryFailureIs502(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("resend-fail-owner@example.test", "correct horse battery")
	s.SeedOrg("resend-fail-org", owner)
	c := s.LoginAs(owner)
	base := s.HTTP.URL + "/api/v1/orgs/resend-fail-org/invites"
	id, oldToken := inviteFor(t, s, c, base, "fail-resend@example.test")
	s.Mail.FailWith(errors.New("550 mailbox unavailable"))

	resp := postJSON(t, c, base+"/"+id+"/resend", nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("resend with a failing mailer: status %d, want 502", resp.StatusCode)
	}
	if body := mustReadBody(t, resp); strings.Contains(body, "550") || strings.Contains(body, "no invitation was created") {
		t.Fatalf("502 body leaks the SMTP answer or says the wrong thing: %s", body)
	}
	if code := acceptInvite(t, s, oldToken); code != http.StatusOK {
		t.Fatalf("old link after a failed resend: status %d, want 200", code)
	}
}

// TestResendInviteNeedsWhatCreateNeeds: members.manage and an interactive
// session, like sending an invitation; an operator and a bearer token are
// refused and nothing is mailed.
func TestResendInviteNeedsWhatCreateNeeds(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("resend-perm-owner@example.test", "correct horse battery")
	org := s.SeedOrg("resend-perm-org", owner)
	operator := s.SeedUser("resend-perm-operator@example.test", "correct horse battery")
	s.AddMember(org, operator, tenancy.RoleOperator)
	base := s.HTTP.URL + "/api/v1/orgs/resend-perm-org/invites"
	id, _ := inviteFor(t, s, s.LoginAs(owner), base, "perm-resend@example.test")
	mailed := len(s.Mail.Sent())

	if resp := postJSON(t, s.LoginAs(operator), base+"/"+id+"/resend", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator resend: status %d, want 403", resp.StatusCode)
	}
	plain, _, err := s.Deps.Tokens.Create(context.Background(), owner, org, "ci", "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := bearerRequestJSON(t, s, http.MethodPost, "/api/v1/orgs/resend-perm-org/invites/"+id+"/resend", plain, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bearer token resend: status %d, want 403", resp.StatusCode)
	}
	if sent := s.Mail.Sent(); len(sent) != mailed {
		t.Fatalf("a refused resend was mailed: %+v", sent[mailed:])
	}
}
