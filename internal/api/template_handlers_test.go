package api_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/playbooks"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type templateDTOBody struct {
	Template struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		PlaybookID string `json:"playbook_id"`
		Scope      struct {
			GroupID *string  `json:"group_id"`
			HostIDs []string `json:"host_ids"`
		} `json:"scope"`
		Limit           string         `json:"limit"`
		ExtraVars       map[string]any `json:"extra_vars"`
		ExtraVarsSchema *struct {
			Fields []map[string]any `json:"fields"`
		} `json:"extra_vars_schema"`
		CredentialID    string              `json:"credential_id"`
		VaultIdentities []vaultIdentityBody `json:"vault_identities"`
	} `json:"template"`
}

// templateFixture is one org with a playbook, two SSH credentials, a group
// and a managed plus an unmanaged host, all seeded through the services
// outside any HTTP session.
type templateFixture struct {
	s         *apitest.Server
	c         *http.Client
	orgID     uuid.UUID
	base      string
	playbook  string
	sshCred   string
	gitCred   string
	vaultCred string
	group     string
	managed   string
	unmanaged string
}

func newTemplateFixture(t *testing.T, slug string) templateFixture {
	t.Helper()
	s := apitest.New(t)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	orgID := s.SeedOrg(slug, owner)
	scoped := db.WithOrgScope(context.Background(), orgID)

	pb, err := s.Deps.Playbooks.CreateEditor(scoped, orgID, playbooks.CreateInput{Inline: "- name: deploy\n  hosts: all\n  tasks: []\n"})
	if err != nil {
		t.Fatal(err)
	}
	sshSvc := s.Deps.Credentials
	sshCred, err := sshSvc.Create(scoped, orgID, credentials.CreateInput{
		Name: "host-login", Kind: credentials.KindSSHKey, Username: "ansible",
		Material: credentials.Material{Generate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	gitCred, err := sshSvc.Create(scoped, orgID, credentials.CreateInput{
		Name: "git-token", Kind: credentials.KindGitToken,
		Material: credentials.Material{Token: "tok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	vaultCred, err := sshSvc.Create(scoped, orgID, credentials.CreateInput{
		Name: "vault", Kind: credentials.KindVaultPassword,
		Material: credentials.Material{Password: "the-vault-passphrase"},
	})
	if err != nil {
		t.Fatal(err)
	}
	zones, err := s.Deps.Hosts.ListZones(scoped, orgID)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	managed, err := s.Deps.Hosts.CreateHost(scoped, orgID, hosts.HostInput{
		Name: "web1", Address: "10.0.0.1", ZoneID: zones[0].ID, Managed: true, Monitored: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	unmanaged, err := s.Deps.Hosts.CreateHost(scoped, orgID, hosts.HostInput{
		Name: "switch1", Address: "10.0.0.2", ZoneID: zones[0].ID, Managed: false, Monitored: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	grp, err := s.Deps.Inventory.CreateGroup(scoped, orgID, inventory.GroupInput{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	return templateFixture{
		s: s, c: s.LoginAs(owner), orgID: orgID,
		base:     s.HTTP.URL + "/api/v1/orgs/" + slug + "/job-templates",
		playbook: pb.ID.String(), sshCred: sshCred.ID.String(), gitCred: gitCred.ID.String(), vaultCred: vaultCred.ID.String(),
		group: grp.ID.String(), managed: managed.ID.String(), unmanaged: unmanaged.ID.String(),
	}
}

func (f templateFixture) body() map[string]any {
	return map[string]any{"name": "deploy-site", "playbook_id": f.playbook}
}

func (f templateFixture) create(t *testing.T, body map[string]any) templateDTOBody {
	t.Helper()
	resp := postJSON(t, f.c, f.base, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var out templateDTOBody
	decodeInto(t, resp, &out)
	return out
}

func TestTemplateLifecycle(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-org")
	secret := "plain-hunter2"
	body := f.body()
	body["limit"] = "web1"
	body["extra_vars"] = map[string]any{"greeting": "hello", "token": secret}
	body["extra_vars_schema"] = map[string]any{"fields": []any{
		map[string]any{"name": "greeting", "type": "string"},
		map[string]any{"name": "token", "type": "secret"},
	}}
	body["credential_id"] = f.sshCred

	created := f.create(t, body)
	tmpl := created.Template
	if tmpl.ID == "" || tmpl.Name != "deploy-site" || tmpl.PlaybookID != f.playbook || tmpl.CredentialID != f.sshCred || tmpl.Limit != "web1" {
		t.Fatalf("created = %+v", tmpl)
	}
	if tmpl.Scope.GroupID != nil || len(tmpl.Scope.HostIDs) != 0 {
		t.Fatalf("scope = %+v, want an empty (whole-org) scope", tmpl.Scope)
	}
	if tmpl.ExtraVars["greeting"] != "hello" {
		t.Fatalf("extra vars = %#v", tmpl.ExtraVars)
	}
	if m, ok := tmpl.ExtraVars["token"].(map[string]any); !ok || m["$secret"] != "set" {
		t.Fatalf("secret extra var must read masked, got %#v", tmpl.ExtraVars["token"])
	}

	resp := requestJSON(t, f.c, http.MethodGet, f.base+"/"+tmpl.ID, nil)
	var got templateDTOBody
	decodeInto(t, resp, &got)
	if resp.StatusCode != http.StatusOK || got.Template.ID != tmpl.ID || got.Template.ExtraVars["greeting"] != "hello" {
		t.Fatalf("get: %d %+v", resp.StatusCode, got)
	}
	if m, ok := got.Template.ExtraVars["token"].(map[string]any); !ok || m["$secret"] != "set" {
		t.Fatalf("get must mask the secret too: %#v", got.Template.ExtraVars["token"])
	}

	resp = requestJSON(t, f.c, http.MethodGet, f.base, nil)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusOK || !strings.Contains(body, "deploy") || strings.Contains(body, secret) {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}

	resp = requestJSON(t, f.c, http.MethodPut, f.base+"/"+tmpl.ID, map[string]any{
		"name": "deploy-renamed", "playbook_id": f.playbook, "limit": "web1:!web2",
		"scope": map[string]any{"host_ids": []string{f.managed}},
	})
	var updated templateDTOBody
	decodeInto(t, resp, &updated)
	if resp.StatusCode != http.StatusOK || updated.Template.Name != "deploy-renamed" || updated.Template.Limit != "web1:!web2" ||
		updated.Template.CredentialID != "" || len(updated.Template.Scope.HostIDs) != 1 {
		t.Fatalf("put: %d %+v", resp.StatusCode, updated)
	}

	resp = requestJSON(t, f.c, http.MethodDelete, f.base+"/"+tmpl.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f.c, http.MethodGet, f.base+"/"+tmpl.ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted: %d", resp.StatusCode)
	}

	resp = requestJSON(t, f.c, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/tmpl-org/activity", nil)
	activity := mustReadBody(t, resp)
	for _, want := range []string{"POST createTemplate", "PUT updateTemplate", "DELETE deleteTemplate", "created job template deploy-site"} {
		if !strings.Contains(activity, want) {
			t.Fatalf("activity missing %q: %s", want, activity)
		}
	}
	if strings.Contains(activity, secret) {
		t.Fatal("activity must never carry an extra-var value")
	}
}

func TestTemplateValidation(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-val")
	other := newTemplateFixture(t, "tmpl-val-other")
	stranger := uuid.Must(uuid.NewV7())

	cases := []struct {
		name string
		body map[string]any
		want int
		msg  string
	}{
		{"bad name", map[string]any{"name": "Deploy!", "playbook_id": f.playbook}, http.StatusBadRequest, "name"},
		{"malformed playbook id", map[string]any{"name": "x", "playbook_id": "nope"}, http.StatusBadRequest, "playbook id"},
		{"missing playbook id", map[string]any{"name": "x"}, http.StatusUnprocessableEntity, ""},
		{"unknown group", func() map[string]any {
			b := f.body()
			b["scope"] = map[string]any{"group_id": uuid.Must(uuid.NewV7()).String()}
			return b
		}(), http.StatusNotFound, ""},
		{"unknown host", func() map[string]any {
			b := f.body()
			b["scope"] = map[string]any{"host_ids": []string{uuid.Must(uuid.NewV7()).String()}}
			return b
		}(), http.StatusNotFound, ""},
		{"unmanaged host", func() map[string]any {
			b := f.body()
			b["scope"] = map[string]any{"host_ids": []string{f.unmanaged}}
			return b
		}(), http.StatusUnprocessableEntity, "managed"},
		{"malformed host id", func() map[string]any {
			b := f.body()
			b["scope"] = map[string]any{"host_ids": []string{"nope"}}
			return b
		}(), http.StatusBadRequest, "scope.host_ids"},
		{"var violates schema", func() map[string]any {
			b := f.body()
			b["extra_vars_schema"] = map[string]any{"fields": []any{map[string]any{"name": "greeting", "type": "string"}}}
			b["extra_vars"] = map[string]any{"greeting": 123}
			return b
		}(), http.StatusUnprocessableEntity, "validation failed"},
		// Re-review N1: extra vars carry ansible's highest precedence; a
		// schema-less template must not switch host key checking off.
		{"connection-security extra var", func() map[string]any {
			b := f.body()
			delete(b, "extra_vars_schema")
			b["extra_vars"] = map[string]any{"ansible_ssh_args": "-o StrictHostKeyChecking=no"}
			return b
		}(), http.StatusUnprocessableEntity, "body.extra_vars.ansible_ssh_args"},
		{"malformed schema", func() map[string]any {
			b := f.body()
			b["extra_vars_schema"] = map[string]any{"fields": []any{map[string]any{"name": "Greeting", "type": "string"}}}
			return b
		}(), http.StatusUnprocessableEntity, ""},
		{"non-ssh credential", func() map[string]any {
			b := f.body()
			b["credential_id"] = f.gitCred
			return b
		}(), http.StatusUnprocessableEntity, "SSH"},
		{"cross-org playbook", func() map[string]any {
			b := f.body()
			b["playbook_id"] = other.playbook
			return b
		}(), http.StatusNotFound, ""},
		{"cross-org credential", func() map[string]any {
			b := f.body()
			b["credential_id"] = other.sshCred
			return b
		}(), http.StatusNotFound, ""},
		{"over-long limit", func() map[string]any {
			b := f.body()
			b["limit"] = strings.Repeat("a", 501)
			return b
		}(), http.StatusUnprocessableEntity, "expected length <= 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, f.c, f.base, tc.body)
			body := mustReadBody(t, resp)
			if resp.StatusCode != tc.want || (tc.msg != "" && !strings.Contains(body, tc.msg)) {
				t.Fatalf("status %d, want %d (msg %q): %s", resp.StatusCode, tc.want, tc.msg, body)
			}
		})
	}

	// Problem paths survive to the response for value problems and for the
	// schema document itself, at their respective locations.
	resp := postJSON(t, f.c, f.base, map[string]any{
		"name": "paths", "playbook_id": f.playbook,
		"extra_vars_schema": map[string]any{"fields": []any{map[string]any{"name": "greeting", "type": "string"}}},
		"extra_vars":        map[string]any{"greeting": 123},
	})
	hasLocation(t, resp, "body.extra_vars.greeting")
	resp = postJSON(t, f.c, f.base, map[string]any{
		"name": "paths2", "playbook_id": f.playbook,
		"extra_vars_schema": map[string]any{"fields": []any{map[string]any{"name": "Greeting", "type": "string"}}},
	})
	hasLocation(t, resp, "body.extra_vars_schema.fields[0].name")

	// Name clashes within the org are conflicts; a stranger's id 404s.
	created := f.create(t, f.body())
	if resp := postJSON(t, f.c, f.base, f.body()); resp.StatusCode != http.StatusConflict {
		t.Fatalf("name clash: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, f.c, http.MethodGet, f.base+"/"+stranger.String(), nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, f.c, http.MethodPut, f.base+"/"+created.Template.ID, map[string]any{
		"name": "deploy-site", "playbook_id": f.playbook,
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("put keeping own name: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestTemplatePermissionsAndCrossOrgIsolation(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-perm-org")
	// stranger is another org's fully seeded fixture; its owner sees none of
	// this org's templates.
	stranger := newTemplateFixture(t, "tmpl-perm-stranger")

	if resp := postJSON(t, f.s.Client(), f.base, f.body()); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d", resp.StatusCode)
	}

	viewer := f.s.SeedUser("tmpl-perm-viewer@example.test", "correct horse battery")
	operator := f.s.SeedUser("tmpl-perm-operator@example.test", "correct horse battery")
	f.s.AddMember(f.orgID, viewer, tenancy.RoleViewer)
	f.s.AddMember(f.orgID, operator, tenancy.RoleOperator)

	vc := f.s.LoginAs(viewer)
	if resp := requestJSON(t, vc, http.MethodGet, f.base, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer list: %d", resp.StatusCode)
	}
	if resp := postJSON(t, vc, f.base, f.body()); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, vc, http.MethodDelete, f.base+"/"+uuid.Must(uuid.NewV7()).String(), nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer delete: %d", resp.StatusCode)
	}

	oc := f.s.LoginAs(operator)
	resp := postJSON(t, oc, f.base, f.body())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("operator create: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var created templateDTOBody
	decodeInto(t, resp, &created)
	if resp := requestJSON(t, oc, http.MethodPut, f.base+"/"+created.Template.ID, map[string]any{
		"name": "operator-renamed", "playbook_id": f.playbook,
	}); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator put: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, oc, http.MethodDelete, f.base+"/"+created.Template.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator delete: %d", resp.StatusCode)
	}

	// A template id from another org is invisible to its owner — exactly
	// like one that does not exist.
	if resp := requestJSON(t, stranger.c, http.MethodGet, stranger.base+"/"+created.Template.ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-org read: %d", resp.StatusCode)
	}
}

// TestTemplateVaultIdentitiesRoundTrip is the brief's Step 3: create a
// template with two labelled vault credentials, read the values back (not
// only the count), then update down to one and assert the dropped label is
// gone, on every read (GET and the PUT's own response).
func TestTemplateVaultIdentitiesRoundTrip(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-vault")
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	otherVault, err := f.s.Deps.Credentials.Create(scoped, f.orgID, credentials.CreateInput{
		Name: "vault-2", Kind: credentials.KindVaultPassword,
		Material: credentials.Material{Password: "another-passphrase"},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := f.body()
	body["vault_identities"] = []map[string]any{
		{"credential_id": f.vaultCred, "label": "team-a"},
		{"credential_id": otherVault.ID.String(), "label": "team-b"},
	}
	created := f.create(t, body)
	wantCreated := []vaultIdentityBody{
		{CredentialID: f.vaultCred, Label: "team-a"},
		{CredentialID: otherVault.ID.String(), Label: "team-b"},
	}
	if !reflect.DeepEqual(created.Template.VaultIdentities, wantCreated) {
		t.Fatalf("created vault identities = %+v, want %+v", created.Template.VaultIdentities, wantCreated)
	}

	resp := requestJSON(t, f.c, http.MethodGet, f.base+"/"+created.Template.ID, nil)
	var got templateDTOBody
	decodeInto(t, resp, &got)
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(got.Template.VaultIdentities, wantCreated) {
		t.Fatalf("get: %d, vault identities = %+v, want %+v", resp.StatusCode, got.Template.VaultIdentities, wantCreated)
	}

	resp = requestJSON(t, f.c, http.MethodPut, f.base+"/"+created.Template.ID, map[string]any{
		"name": "deploy-site", "playbook_id": f.playbook,
		"vault_identities": []map[string]any{{"credential_id": f.vaultCred, "label": "team-a"}},
	})
	var updated templateDTOBody
	decodeInto(t, resp, &updated)
	wantUpdated := []vaultIdentityBody{{CredentialID: f.vaultCred, Label: "team-a"}}
	if resp.StatusCode != http.StatusOK || !reflect.DeepEqual(updated.Template.VaultIdentities, wantUpdated) {
		t.Fatalf("update: %d, vault identities = %+v, want %+v (team-b must be dropped)", resp.StatusCode, updated.Template.VaultIdentities, wantUpdated)
	}

	resp = requestJSON(t, f.c, http.MethodGet, f.base+"/"+created.Template.ID, nil)
	decodeInto(t, resp, &got)
	for _, id := range got.Template.VaultIdentities {
		if id.Label == "team-b" {
			t.Fatalf("team-b must be gone, got %+v", got.Template.VaultIdentities)
		}
	}
}

// TestTemplateVaultIdentityLabelValidation is the brief's Step 2/3: one case
// per character class rejected (status and message), plus a duplicate
// label, a wrong-kind credential and an unknown credential.
func TestTemplateVaultIdentityLabelValidation(t *testing.T) {
	f := newTemplateFixture(t, "tmpl-vault-val")

	cases := []struct {
		name  string
		items []map[string]any
		want  int
		msg   string
	}{
		{"empty label", []map[string]any{{"credential_id": f.vaultCred, "label": ""}}, http.StatusBadRequest, "label is empty"},
		{"leading whitespace", []map[string]any{{"credential_id": f.vaultCred, "label": " team-a"}}, http.StatusBadRequest, "leading or trailing whitespace"},
		{"trailing whitespace", []map[string]any{{"credential_id": f.vaultCred, "label": "team-a "}}, http.StatusBadRequest, "leading or trailing whitespace"},
		{"newline", []map[string]any{{"credential_id": f.vaultCred, "label": "team-a\nhost_key_checking = False"}}, http.StatusBadRequest, "newline"},
		{"carriage return", []map[string]any{{"credential_id": f.vaultCred, "label": "team-a\rx"}}, http.StatusBadRequest, "carriage return"},
		{"comma", []map[string]any{{"credential_id": f.vaultCred, "label": "team-a,team-b"}}, http.StatusBadRequest, "comma"},
		{"at sign", []map[string]any{{"credential_id": f.vaultCred, "label": "team-a@file"}}, http.StatusBadRequest, "'@'"},
		{"duplicate label", []map[string]any{
			{"credential_id": f.vaultCred, "label": "team-a"},
			{"credential_id": f.vaultCred, "label": "team-a"},
		}, http.StatusBadRequest, "duplicate vault credential label"},
		{"wrong credential kind", []map[string]any{{"credential_id": f.sshCred, "label": "team-a"}}, http.StatusBadRequest, "vault_password credential"},
		{"unknown credential", []map[string]any{{"credential_id": "11111111-1111-1111-1111-111111111111", "label": "team-a"}}, http.StatusBadRequest, "vault credential not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := f.body()
			body["vault_identities"] = tc.items
			resp := postJSON(t, f.c, f.base, body)
			respBody := mustReadBody(t, resp)
			if resp.StatusCode != tc.want || !strings.Contains(respBody, tc.msg) {
				t.Fatalf("status %d, want %d naming %q: %s", resp.StatusCode, tc.want, tc.msg, respBody)
			}
		})
	}
	// An overlong label is a schema-level 422 (maxLength), not the
	// service's 400 — huma rejects it before the handler ever runs.
	body := f.body()
	body["vault_identities"] = []map[string]any{{"credential_id": f.vaultCred, "label": strings.Repeat("a", 101)}}
	if resp := postJSON(t, f.c, f.base, body); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("overlong label: %d, want 422", resp.StatusCode)
	}
}
