package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/tenancy"
)

var secretSchema = map[string]any{"fields": []any{
	map[string]any{"name": "app_user", "type": "string"},
	map[string]any{"name": "app_password", "type": "secret"},
}}

// TestInventoryWriteTools walks the setup an assistant does: a host, its
// SSH settings, a group with a schema, its vars, the membership.
func TestInventoryWriteTools(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)

	var zones struct {
		Zones []struct {
			ID string `json:"id"`
		} `json:"zones"`
	}
	callOK(t, cs, "list_zones", nil, &zones)
	if len(zones.Zones) == 0 {
		t.Fatal("no zones")
	}
	var created struct {
		Host struct {
			ID      string `json:"id"`
			Address string `json:"address"`
		} `json:"host"`
	}
	callOK(t, cs, "create_host", map[string]any{"name": "db-1", "address": "10.0.0.5", "zone_id": zones.Zones[0].ID, "managed": true}, &created)
	callOK(t, cs, "update_host", map[string]any{"host_id": created.Host.ID, "fields": map[string]any{"address": "10.0.0.6"}}, &created)
	if created.Host.Address != "10.0.0.6" {
		t.Fatalf("updated host = %+v", created.Host)
	}
	callOK(t, cs, "set_host_ssh", map[string]any{"host_id": created.Host.ID, "username": "root", "port": 2222, "credential_id": f.credential.String()}, nil)

	var group struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	callOK(t, cs, "create_group", map[string]any{"name": "databases"}, &group)
	callOK(t, cs, "update_group", map[string]any{"group_id": group.Group.ID, "fields": map[string]any{"description": "Postgres"}}, nil)
	callOK(t, cs, "set_group_schema", map[string]any{"group_id": group.Group.ID, "schema": secretSchema}, nil)
	callOK(t, cs, "set_group_vars", map[string]any{"group_id": group.Group.ID, "vars": map[string]any{"app_user": "app"}}, nil)
	callOK(t, cs, "add_group_member", map[string]any{"group_id": group.Group.ID, "host_id": created.Host.ID}, nil)

	text := callOK(t, cs, "get_group", map[string]any{"group_id": group.Group.ID}, nil)
	for _, want := range []string{"Postgres", "app_password", `"app_user": "app"`, created.Host.ID} {
		if !strings.Contains(text, want) {
			t.Errorf("get_group lacks %q:\n%s", want, text)
		}
	}
	callOK(t, cs, "remove_group_member", map[string]any{"group_id": group.Group.ID, "host_id": created.Host.ID}, nil)
}

// TestSecretFieldsNeverGetAValue: a value under a secret-typed field is
// refused through every vars path, and nothing is stored (Review Focus 2).
func TestSecretFieldsNeverGetAValue(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	g := f.group.String()
	callOK(t, cs, "set_group_schema", map[string]any{"group_id": g, "schema": map[string]any{"fields": []any{
		map[string]any{"name": "app_password", "type": "secret"},
		map[string]any{"name": "db", "type": "object", "fields": []any{map[string]any{"name": "token", "type": "secret"}}},
		map[string]any{"name": "host_secret", "type": "secret", "scope": "host"},
	}}}, nil)
	refusals := []struct {
		tool string
		args map[string]any
	}{
		{"set_group_vars", map[string]any{"group_id": g, "vars": map[string]any{"app_password": "hunter2"}}},
		{"set_group_vars", map[string]any{"group_id": g, "vars": map[string]any{"db": map[string]any{"token": "abc"}}}},
		{"add_group_member", map[string]any{"group_id": g, "host_id": f.host.String(), "vars": map[string]any{"host_secret": "x"}}},
		{"create_job_template", map[string]any{"name": "t2", "playbook_id": f.playbook.String(),
			"extra_vars_schema": map[string]any{"fields": []any{map[string]any{"name": "pw", "type": "secret"}}},
			"extra_vars":        map[string]any{"pw": "hunter2"}}},
		{"update_job_template", map[string]any{"template_id": f.template.String(), "fields": map[string]any{
			"extra_vars_schema": map[string]any{"fields": []any{map[string]any{"name": "pw", "type": "secret"}}},
			"extra_vars":        map[string]any{"pw": "hunter2"}}}},
	}
	for _, r := range refusals {
		text, isErr := call(t, cs, r.tool, r.args)
		if !isErr || !strings.Contains(text, "secrets are set by a person in Pulsix") {
			t.Errorf("%s %v = %v %s, want the secret refusal", r.tool, r.args, isErr, text)
		}
	}
	// Nothing was stored.
	gv, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.org, f.group)
	if err != nil {
		t.Fatal(err)
	}
	if len(gv.Vars) != 0 {
		t.Fatalf("group vars after the refusals = %v", gv.Vars)
	}
	// Leaving the secret out is fine; a person fills it in.
	callOK(t, cs, "set_group_vars", map[string]any{"group_id": g, "vars": map[string]any{}}, nil)
}

func TestSchemaChangesKeepSecretTypes(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	g := f.group.String()
	callOK(t, cs, "set_group_schema", map[string]any{"group_id": g, "schema": secretSchema}, nil)
	text, isErr := call(t, cs, "set_group_schema", map[string]any{"group_id": g, "schema": map[string]any{"fields": []any{
		map[string]any{"name": "app_user", "type": "string"},
		map[string]any{"name": "app_password", "type": "string"},
	}}})
	if !isErr || !strings.Contains(text, "secret fields keep their type") {
		t.Fatalf("dropping the secret type = %v %s", isErr, text)
	}
	text, isErr = call(t, cs, "set_group_schema", map[string]any{"group_id": g, "schema": map[string]any{"fields": []any{
		map[string]any{"name": "app_user", "type": "string"},
	}}})
	if !isErr || !strings.Contains(text, "app_password") {
		t.Fatalf("removing a secret field = %v %s", isErr, text)
	}
	gv, _ := f.s.Deps.Inventory.GetGroup(f.scoped, f.org, f.group)
	if len(gv.Schema.Fields) != 2 || gv.Schema.Fields[1].Type != schema.TypeSecret {
		t.Fatalf("schema after the refusals = %+v", gv.Schema)
	}
}

func TestRepoPlaybookAndTemplateWriteTools(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	var repo struct {
		Repo struct {
			ID      string `json:"id"`
			Subpath string `json:"subpath"`
		} `json:"repo"`
	}
	callOK(t, cs, "create_repo", map[string]any{"name": "noidee-cloud", "url": "https://git.example.test/noidee-cloud.git", "subpath": "ansible"}, &repo)
	callOK(t, cs, "update_repo", map[string]any{"repo_id": repo.Repo.ID, "fields": map[string]any{"branch": "develop"}}, nil)
	got, err := f.s.Deps.Repos.List(f.scoped, f.org)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range got {
		if r.Name == "noidee-cloud" && r.Branch == "develop" && r.Subpath == "ansible" {
			found = true
		}
	}
	if !found {
		t.Fatalf("repos = %+v", got)
	}
	// Sync enqueues a job and touches no host; the route answers either way.
	if text, isErr := call(t, cs, "sync_repo", map[string]any{"repo_id": repo.Repo.ID}); isErr && !strings.Contains(text, "sync") {
		t.Fatalf("sync_repo = %s", text)
	}
	if text, isErr := call(t, cs, "create_playbook", map[string]any{"repo_id": repo.Repo.ID, "path": "../../etc/passwd"}); !isErr {
		t.Fatalf("a path leaving the repository was accepted: %s", text)
	}

	var tmpl struct {
		Template struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		} `json:"template"`
	}
	callOK(t, cs, "create_job_template", map[string]any{
		"name": "core", "playbook_id": f.playbook.String(), "limit": "web-1",
		"scope":            map[string]any{"group_id": f.group.String()},
		"vault_identities": []any{map[string]any{"label": "noidee", "credential_id": f.vault.String()}},
	}, &tmpl)
	stale := tmpl.Template.Revision
	callOK(t, cs, "update_job_template", map[string]any{"template_id": tmpl.Template.ID, "fields": map[string]any{"limit": "web-*"}}, nil)
	text, isErr := call(t, cs, "update_job_template", map[string]any{"template_id": tmpl.Template.ID, "revision": stale, "fields": map[string]any{"limit": "x"}})
	if !isErr || !strings.Contains(text, "changed meanwhile; read it again") {
		t.Fatalf("a stale revision = %v %s", isErr, text)
	}
	if text, isErr := call(t, cs, "update_job_template", map[string]any{"template_id": tmpl.Template.ID, "fields": map[string]any{"bogus": 1}}); !isErr || !strings.Contains(text, "bogus") {
		t.Fatalf("an unknown field = %v %s", isErr, text)
	}
}

// TestWriteToolsNeedTheirArea: a read-only token sees no write tool and
// cannot call one.
func TestWriteToolsNeedTheirArea(t *testing.T) {
	f := newOrgFixture(t, tenancy.AreaInventoryRead, tenancy.AreaReposRead, tenancy.AreaTemplatesRead)
	cs := mustConnect(t, f.s, f.token)
	names := toolNames(t, cs)
	for _, tool := range []string{"create_host", "update_host", "set_host_ssh", "create_group", "update_group", "set_group_schema",
		"set_group_vars", "add_group_member", "remove_group_member", "create_repo", "update_repo", "sync_repo", "create_playbook",
		"create_job_template", "update_job_template"} {
		if names[tool] {
			t.Errorf("%s listed for a read-only token", tool)
		}
		if _, isErr := call(t, cs, tool, map[string]any{}); !isErr {
			t.Errorf("%s ran for a read-only token", tool)
		}
	}
	// The read tools are there.
	if !names["get_group"] || !names["list_zones"] {
		t.Errorf("read tools missing: %v", names)
	}
	_ = json.Valid
	_ = context.Background
}

// TestFieldBecomesSecretOnlyWhileEmpty: the assistant cannot choose a
// secret's value by writing a plain value first and marking the field
// secret afterwards — through group vars, membership vars or template
// extra vars. A field that holds no value may still become secret.
func TestFieldBecomesSecretOnlyWhileEmpty(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	g := f.group.String()
	plain := map[string]any{"fields": []any{
		map[string]any{"name": "x", "type": "string"},
		map[string]any{"name": "y", "type": "string", "scope": "host"},
		map[string]any{"name": "z", "type": "string"},
	}}
	callOK(t, cs, "set_group_schema", map[string]any{"group_id": g, "schema": plain}, nil)
	callOK(t, cs, "set_group_vars", map[string]any{"group_id": g, "vars": map[string]any{"x": "chosen-by-assistant"}}, nil)
	callOK(t, cs, "add_group_member", map[string]any{"group_id": g, "host_id": f.host.String(), "vars": map[string]any{"y": "chosen-by-assistant"}}, nil)
	flip := func(name string) map[string]any {
		fields := []any{}
		for _, n := range []string{"x", "y", "z"} {
			fd := map[string]any{"name": n, "type": "string"}
			if n == "y" {
				fd["scope"] = "host"
			}
			if n == name {
				fd["type"] = "secret"
			}
			fields = append(fields, fd)
		}
		return map[string]any{"group_id": g, "schema": map[string]any{"fields": fields}}
	}
	for _, name := range []string{"x", "y"} {
		if text, isErr := call(t, cs, "set_group_schema", flip(name)); !isErr || !strings.Contains(text, "holds no value") {
			t.Errorf("marking %s secret while it holds the assistant's value = %v %s", name, isErr, text)
		}
	}
	gv, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.org, f.group)
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range gv.Schema.Fields {
		if fd.Type == schema.TypeSecret {
			t.Fatalf("a refused change still made %s secret", fd.Name)
		}
	}
	// z holds nothing: it may become secret.
	callOK(t, cs, "set_group_schema", flip("z"), nil)

	tmpl := f.template.String()
	callOK(t, cs, "update_job_template", map[string]any{"template_id": tmpl, "fields": map[string]any{
		"extra_vars_schema": map[string]any{"fields": []any{map[string]any{"name": "pw", "type": "string"}}},
		"extra_vars":        map[string]any{"pw": "chosen-by-assistant"}}}, nil)
	text, isErr := call(t, cs, "update_job_template", map[string]any{"template_id": tmpl, "fields": map[string]any{
		"extra_vars_schema": map[string]any{"fields": []any{map[string]any{"name": "pw", "type": "secret"}}},
		"extra_vars":        map[string]any{"pw": map[string]any{"$secret": "keep"}}}})
	if !isErr || !strings.Contains(text, "holds no value") {
		t.Fatalf("marking a template var secret while it holds the assistant's value = %v %s", isErr, text)
	}
	// Cleared first, it may become secret.
	callOK(t, cs, "update_job_template", map[string]any{"template_id": tmpl, "fields": map[string]any{"extra_vars": map[string]any{}}}, nil)
	callOK(t, cs, "update_job_template", map[string]any{"template_id": tmpl, "fields": map[string]any{
		"extra_vars_schema": map[string]any{"fields": []any{map[string]any{"name": "pw", "type": "secret"}}}}}, nil)
}

// TestSchemaToolsDescribeFieldTypesAlike: update_job_template says what an
// extra_vars_schema holds in the same words as set_group_schema, so an
// assistant learns secret and repeater from either tool.
func TestSchemaToolsDescribeFieldTypesAlike(t *testing.T) {
	f := newOrgFixture(t)
	cs := mustConnect(t, f.s, f.token)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	const words = "fields with name, type (string, int, float, bool, enum, secret, list, repeater, object), scope (group or host) and more"
	desc := map[string]string{}
	for _, tl := range res.Tools {
		desc[tl.Name] = tl.Description
		if tl.Name == "create_job_template" {
			raw, err := json.Marshal(tl.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), words) {
				t.Errorf("create_job_template extra_vars_schema property lacks the words: %s", raw)
			}
		}
	}
	for _, name := range []string{"set_group_schema", "update_job_template"} {
		for _, want := range []string{words, "secret", "repeater"} {
			if !strings.Contains(desc[name], want) {
				t.Errorf("%s description lacks %q: %q", name, want, desc[name])
			}
		}
	}
}
