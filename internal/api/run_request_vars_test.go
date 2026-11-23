package api_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

// secretGroup gives the fixture's host a group with a secret group var
// (db_pass) and a secret membership var (host_pass), and returns the group.
func (f runRequestFixture) secretGroup(t *testing.T, groupSecret, memberSecret string) inventory.GroupView {
	t.Helper()
	g, err := f.s.Deps.Inventory.CreateGroup(f.scoped, f.orgID, inventory.GroupInput{Name: "secrets"})
	if err != nil {
		t.Fatal(err)
	}
	g, _, err = f.s.Deps.Inventory.SetSchema(f.scoped, f.orgID, g.ID, g.Revision, schema.Schema{Fields: []schema.Field{
		{Name: "db_pass", Type: schema.TypeSecret, Scope: schema.ScopeGroup},
		{Name: "host_pass", Type: schema.TypeSecret, Scope: schema.ScopeHost},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if g, err = f.s.Deps.Inventory.SetVars(f.scoped, f.orgID, g.ID, g.Revision, map[string]any{"db_pass": groupSecret}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Inventory.Assign(f.scoped, f.orgID, g.ID, f.hostA, map[string]any{"host_pass": memberSecret}, nil); err != nil {
		t.Fatal(err)
	}
	return g
}

func secretExtraVars(value string) func(*templates.UpdateInput) {
	return func(in *templates.UpdateInput) {
		in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "api_key", Type: schema.TypeSecret}}}
		in.ExtraVars = map[string]any{"api_key": value}
	}
}

// TestRunRequestApprovalCoversSecretValues: a secret value that changes
// between the detail and the click refuses the approval, although the
// page shows every secret masked, and no secret ever reaches the detail.
func TestRunRequestApprovalCoversSecretValues(t *testing.T) {
	const groupSecret, memberSecret, extraSecret = "group-s3cret-5e21", "member-s3cret-a8c4", "extra-s3cret-0d9f"
	cases := map[string]func(t *testing.T, f runRequestFixture, g inventory.GroupView){
		"secret group var": func(t *testing.T, f runRequestFixture, g inventory.GroupView) {
			if _, err := f.s.Deps.Inventory.SetVars(f.scoped, f.orgID, g.ID, g.Revision, map[string]any{"db_pass": "group-s3cret-changed"}); err != nil {
				t.Fatal(err)
			}
		},
		"secret membership var": func(t *testing.T, f runRequestFixture, g inventory.GroupView) {
			if _, err := f.s.Deps.Inventory.Assign(f.scoped, f.orgID, g.ID, f.hostA, map[string]any{"host_pass": "member-s3cret-changed"}, nil); err != nil {
				t.Fatal(err)
			}
		},
		"secret extra var": func(t *testing.T, f runRequestFixture, _ inventory.GroupView) {
			// Every template write moves the revision, which the picture
			// covers on its own; put it back so only the value differs.
			rev := f.getTemplate(t, f.templateID).Template.Revision
			updateTemplate(t, f, secretExtraVars("extra-s3cret-changed"))
			if _, err := f.s.DB.Pool().Exec(context.Background(), `UPDATE job_templates SET revision = $1 WHERE id = $2`, rev, f.templateID); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRunRequestFixture(t, "rr-secret-"+strings.ReplaceAll(name, " ", "-"))
			g := f.secretGroup(t, groupSecret, memberSecret)
			updateTemplate(t, f, secretExtraVars(extraSecret))
			created := f.request(t, map[string]any{"reason": "x"})
			resp := requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+f.orgPath()+"/run-requests/"+created.Request.ID, nil)
			body := mustReadBody(t, resp)
			for _, secret := range []string{groupSecret, memberSecret, extraSecret} {
				if strings.Contains(body, secret) {
					t.Fatalf("the detail carries %q: %s", secret, body)
				}
			}
			d := f.detail(t, created.Request.ID)
			change(t, f, g)
			resp = f.approve(t, f.adminC, created.Request.ID, d.Fingerprint)
			if got := mustReadBody(t, resp); resp.StatusCode != http.StatusConflict || !strings.Contains(got, "changed since you looked at it") {
				t.Fatalf("approve after a %s change = %d %s, want 409 changed", name, resp.StatusCode, got)
			}
			if resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint); resp.StatusCode != http.StatusOK {
				t.Fatalf("approve after reloading = %d %s", resp.StatusCode, mustReadBody(t, resp))
			}
		})
	}
}

// TestRunRequestAuditNamesTheApprovedRevision: a template edited after
// the request was made, then reviewed and approved, is audited with the
// revision the person approved.
func TestRunRequestAuditNamesTheApprovedRevision(t *testing.T) {
	f := newRunRequestFixture(t, "rr-audit-rev")
	created := f.request(t, map[string]any{"reason": "x"})
	requested := f.getTemplate(t, f.templateID).Template.Revision
	updateTemplate(t, f, func(in *templates.UpdateInput) { in.Limit = "web-*" })
	approvedRev := f.getTemplate(t, f.templateID).Template.Revision
	if approvedRev == requested {
		t.Fatalf("the edit left the revision at %d", requested)
	}
	if resp := f.approve(t, f.adminC, created.Request.ID, f.detail(t, created.Request.ID).Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if got, want := f.approvalSummary(t, created.Request.ID), "approved run request (template revision "+strconv.FormatInt(approvedRev, 10)+")"; got != want {
		t.Fatalf("audit summary = %q, want %q", got, want)
	}
}
