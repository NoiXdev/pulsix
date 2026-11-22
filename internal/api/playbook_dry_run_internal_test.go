package api

import (
	"errors"
	"slices"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// TestAuthorizeDryRun: jobs.run starts a dry run; a draft also needs
// playbooks.write, since it is an edit that has not been saved. No role
// holds one without the other today, so the rule is pinned here with a
// principal that only has jobs.run.
func TestAuthorizeDryRun(t *testing.T) {
	onlyRun := func(asked *[]tenancy.Permission) func(tenancy.Permission) (Scope, error) {
		return func(p tenancy.Permission) (Scope, error) {
			*asked = append(*asked, p)
			if p == tenancy.PermJobsRun {
				return Scope{OrgID: uuid.Must(uuid.NewV7())}, nil
			}
			return Scope{}, huma.Error403Forbidden("insufficient role")
		}
	}
	var asked []tenancy.Permission
	if _, err := authorizeDryRun(onlyRun(&asked), false); err != nil {
		t.Fatalf("without a draft: %v", err)
	}
	if !slices.Equal(asked, []tenancy.Permission{tenancy.PermJobsRun}) {
		t.Fatalf("asked %v", asked)
	}
	asked = nil
	_, err := authorizeDryRun(onlyRun(&asked), true)
	var se huma.StatusError
	if !errors.As(err, &se) || se.GetStatus() != 403 {
		t.Fatalf("with a draft: err = %v, want 403", err)
	}
	if !slices.Equal(asked, []tenancy.Permission{tenancy.PermJobsRun, tenancy.PermPlaybooksWrite}) {
		t.Fatalf("asked %v", asked)
	}
}

func TestDryRunRequest(t *testing.T) {
	playbook, actor, tmpl, host := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	off := false
	draft := "- hosts: all\n"

	var body playbookDryRunBody
	body.TemplateID = tmpl.String()
	gotTmpl, req, err := dryRunRequest(playbook, actor, body)
	if err != nil {
		t.Fatal(err)
	}
	ov := req.Overrides
	if gotTmpl != tmpl || req.Trigger != "manual" || req.Actor == nil || *req.Actor != actor || req.PlaybookID == nil || *req.PlaybookID != playbook ||
		ov == nil || !ov.Check || !ov.Diff || ov.Tags != nil || ov.Playbook != nil || req.OnlyHosts != nil || req.FollowUps != nil {
		t.Fatalf("defaults: template %s, request %+v, overrides %+v", gotTmpl, req, ov)
	}

	body = playbookDryRunBody{TemplateID: tmpl.String(), HostIDs: []string{host.String()}, Tags: []string{"web"}, Diff: &off, Draft: &draft}
	_, req, err = dryRunRequest(playbook, actor, body)
	if err != nil {
		t.Fatal(err)
	}
	ov = req.Overrides
	if !ov.Check || ov.Diff || !slices.Equal(ov.Tags, []string{"web"}) || ov.Playbook == nil || *ov.Playbook != draft || !slices.Equal(req.OnlyHosts, []uuid.UUID{host}) {
		t.Fatalf("explicit: request %+v, overrides %+v", req, ov)
	}

	for _, bad := range []playbookDryRunBody{{TemplateID: "nope"}, {TemplateID: tmpl.String(), HostIDs: []string{"nope"}}} {
		if _, _, err := dryRunRequest(playbook, actor, bad); err == nil {
			t.Fatalf("%+v: want an error", bad)
		}
	}
}
