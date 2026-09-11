package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/mail"
)

type inviteCreated struct {
	Email     string `json:"email"`
	MailSent  bool   `json:"mail_sent"`
	AcceptURL string `json:"accept_url"`
}

// TestInviteWithoutMailReturnsTheAcceptLink covers inviting on an instance
// whose operator skipped outgoing mail: creating the invitation succeeds,
// the response carries the acceptance link instead of claiming a mail went
// out, and that link really admits the invitee.
func TestInviteWithoutMailReturnsTheAcceptLink(t *testing.T) {
	s := apitest.New(t)
	s.Mail.FailWith(fmt.Errorf("load smtp settings: %w", mail.ErrNotConfigured))
	owner := s.SeedUser("nomail-owner@example.test", "correct horse battery")
	s.SeedOrg("nomail-org", owner)

	resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+"/api/v1/orgs/nomail-org/invites", map[string]any{"email": "nomail@example.test", "role": "operator"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite without mail: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created inviteCreated
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.MailSent || !strings.HasPrefix(created.AcceptURL, s.HTTP.URL+"/invite/") {
		t.Fatalf("invite = %+v, want mail_sent=false and an accept_url under %s/invite/", created, s.HTTP.URL)
	}

	token := strings.TrimPrefix(created.AcceptURL, s.HTTP.URL+"/invite/")
	resp = postJSON(t, s.Client(), s.HTTP.URL+"/api/v1/invites/accept", map[string]any{
		"token": token, "display_name": "No Mail", "password": "correct horse battery",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept the returned link: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var accepted struct {
		SignedIn bool `json:"signed_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if !accepted.SignedIn {
		t.Fatal("accepting a new-account invite did not report signed_in")
	}
}

// TestInviteWithMailDoesNotReturnTheLink: when the mail goes out, the link
// travels only in it.
func TestInviteWithMailDoesNotReturnTheLink(t *testing.T) {
	s := apitest.New(t)
	owner := s.SeedUser("mail-owner@example.test", "correct horse battery")
	s.SeedOrg("mail-org", owner)

	resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+"/api/v1/orgs/mail-org/invites", map[string]any{"email": "mailed@example.test", "role": "viewer"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create invite: status %d, body %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created inviteCreated
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !created.MailSent || created.AcceptURL != "" {
		t.Fatalf("invite = %+v, want mail_sent=true and no accept_url", created)
	}
}

// TestInviteDeliveryFailureIsAClearErrorAndLeavesNoInvitation: with mail
// configured but failing, the answer is a specific 502 — not an opaque 500 —
// that does not leak the SMTP server's own error text, no invitation row is
// left behind, and no audit event claims one was created.
func TestInviteDeliveryFailureIsAClearErrorAndLeavesNoInvitation(t *testing.T) {
	s := apitest.New(t)
	s.Mail.FailWith(errors.New("dial tcp 10.1.2.3:25: connect: connection refused"))
	owner := s.SeedUser("failmail-owner@example.test", "correct horse battery")
	org := s.SeedOrg("failmail-org", owner)

	resp := postJSON(t, s.LoginAs(owner), s.HTTP.URL+"/api/v1/orgs/failmail-org/invites", map[string]any{"email": "lost@example.test", "role": "viewer"})
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("create invite with failing mail: status %d, want 502; body %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "10.1.2.3") || !strings.Contains(body, "could not be delivered") {
		t.Fatalf("error body = %s, want a specific message without the SMTP server's own error", body)
	}

	ctx := context.Background()
	var invites, events int
	if err := s.DB.Pool().QueryRow(ctx, `SELECT count(*) FROM invites WHERE org_id = $1`, org).Scan(&invites); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE org_id = $1 AND object_type = 'invite'`, org).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if invites != 0 || events != 0 {
		t.Fatalf("after a failed delivery: %d invitation(s), %d invite audit event(s); want none", invites, events)
	}
}
