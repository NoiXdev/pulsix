package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// templateVariablesBody decodes GET/PUT …/job-templates/{id}/variables.
type templateVariablesBody struct {
	ExtraVarsSchema *struct {
		Fields []map[string]any `json:"fields"`
	} `json:"extra_vars_schema"`
	ExtraVars map[string]any `json:"extra_vars"`
	Revision  int64          `json:"revision"`
	Mode      string         `json:"mode"`
}

// TestTemplateVariablesEndpoint: a template's variables have a read
// (templates.read, secrets masked, the revision as ETag) and a write
// (templates.write, optional If-Match, audited) of their own. A value under
// an inactive field the body omits stays stored; a projection's variables
// belong to its definition.
func TestTemplateVariablesEndpoint(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-vars-org")
	body := f.body()
	body["extra_vars_schema"] = map[string]any{"fields": []any{
		map[string]any{"name": "region", "type": "string"},
		map[string]any{"name": "token", "type": "secret"},
	}}
	body["extra_vars"] = map[string]any{"region": "eu", "token": "plain-hunter2"}
	created := f.create(t, body)
	url := f.base + "/" + created.Template.ID + "/variables"

	resp := requestJSON(t, f.c, http.MethodGet, url, nil)
	var got templateVariablesBody
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("get: %d ETag %q %s", resp.StatusCode, resp.Header.Get("ETag"), mustReadBody(t, resp))
	}
	decodeInto(t, resp, &got)
	if got.Mode != "off" || got.Revision != 1 || got.ExtraVars["region"] != "eu" || got.ExtraVarsSchema == nil || len(got.ExtraVarsSchema.Fields) != 2 {
		t.Fatalf("get = %+v", got)
	}
	if m, ok := got.ExtraVars["token"].(map[string]any); !ok || m["$secret"] != "set" {
		t.Fatalf("token = %#v, want it masked", got.ExtraVars["token"])
	}

	// token switched inactive and left out of the body: kept.
	put := map[string]any{
		"extra_vars_schema": map[string]any{"fields": []any{
			map[string]any{"name": "region", "type": "string"},
			map[string]any{"name": "token", "type": "secret", "active": false},
		}},
		"extra_vars": map[string]any{"region": "eu-west"},
	}
	resp = requestWithHeaders(t, f.c, http.MethodPut, url, put, map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("put If-Match 1: %d ETag %q %s", resp.StatusCode, resp.Header.Get("ETag"), mustReadBody(t, resp))
	}
	decodeInto(t, resp, &got)
	if got.Revision != 2 || got.ExtraVars["region"] != "eu-west" {
		t.Fatalf("put = %+v", got)
	}
	if m, ok := got.ExtraVars["token"].(map[string]any); !ok || m["$secret"] != "set" {
		t.Fatalf("token after an inactive save = %#v, want it kept and masked", got.ExtraVars["token"])
	}

	resp = requestWithHeaders(t, f.c, http.MethodPut, url, put, map[string]string{"If-Match": `"1"`})
	if got := mustReadBody(t, resp); resp.StatusCode != http.StatusPreconditionFailed || !strings.Contains(got, "reload") {
		t.Fatalf("put stale: %d %s, want 412", resp.StatusCode, got)
	}
	if resp := requestJSON(t, f.c, http.MethodPut, url, put); resp.StatusCode != http.StatusOK {
		t.Fatalf("put without If-Match: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	bad := map[string]any{"extra_vars_schema": put["extra_vars_schema"], "extra_vars": map[string]any{"unknown": "x"}}
	if resp := requestJSON(t, f.c, http.MethodPut, url, bad); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("put an undeclared value: %d %s, want 422", resp.StatusCode, mustReadBody(t, resp))
	}

	viewer := f.s.SeedUser("tmpl-vars-viewer@example.test", "correct horse battery")
	f.s.AddMember(f.orgID, viewer, tenancy.RoleViewer)
	vc := f.s.LoginAs(viewer)
	if resp := requestJSON(t, vc, http.MethodGet, url, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer get: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, vc, http.MethodPut, url, put); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer put: %d, want 403", resp.StatusCode)
	}
	if resp := requestJSON(t, f.c, http.MethodGet, f.base+"/01a1ab27-0000-7000-8000-000000000000/variables", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get unknown: %d, want 404", resp.StatusCode)
	}

	resp = requestJSON(t, f.c, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/tmpl-vars-org/activity", nil)
	if activity := mustReadBody(t, resp); !strings.Contains(activity, "PUT updateTemplateVariables") || strings.Contains(activity, "plain-hunter2") {
		t.Fatalf("activity = %s, want the variables write audited without values", activity)
	}
}

// TestTemplateVariablesRefusedOnAProjection: a projected template's
// variables belong to the installation-owned definition — 400.
func TestTemplateVariablesRefusedOnAProjection(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-vars-proj-org")
	admin := f.s.SeedUser("tmpl-vars-proj-admin@example.test", "correct horse battery")
	makeInstanceAdmin(t, f.s, admin)
	adminClient := f.s.LoginAs(admin)
	repoDefID := seedRepoDefinition(t, adminClient, f.s, "infra-for-tmpl-vars")
	resp := postJSON(t, adminClient, f.s.HTTP.URL+"/api/v1/admin/templates/definitions", map[string]any{
		"name": "shared-deploy", "shared_repo_id": repoDefID, "playbook_path": "site.yml",
		"extra_vars":        map[string]any{"region": "eu"},
		"extra_vars_schema": map[string]any{"fields": []any{map[string]any{"name": "region", "type": "string"}}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create definition: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, f.c, http.MethodGet, f.base, nil)
	var list markedTemplateListBody
	decodeInto(t, resp, &list)
	projID := ""
	for _, tm := range list.Templates {
		if tm.SharedTemplateID != "" {
			projID = tm.ID
		}
	}
	if projID == "" {
		t.Fatalf("no projection in %+v", list.Templates)
	}
	url := f.base + "/" + projID + "/variables"
	if resp := requestJSON(t, f.c, http.MethodGet, url, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("get a projection's variables: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp = requestJSON(t, f.c, http.MethodPut, url, map[string]any{
		"extra_vars_schema": map[string]any{"fields": []any{map[string]any{"name": "region", "type": "string"}}},
		"extra_vars":        map[string]any{"region": "us"},
	})
	if got := mustReadBody(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(got, "installation") {
		t.Fatalf("put a projection's variables: %d %s, want 400", resp.StatusCode, got)
	}
}
