package mcp_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestNoToolOutputContainsASecret seeds secrets of every kind the tools sit
// next to, calls every tool with arguments that reach that data, and
// asserts no output carries any of them (Review Focus 1).
func TestNoToolOutputContainsASecret(t *testing.T) {
	f := newOrgFixture(t)
	markers := []string{"vault-passphrase-7f3a", "sweep-ssh-password-91c2", "sweep-git-token-55aa", "sweep-group-secret-3e1f", "sweep-template-secret-8b0d"}

	if _, err := f.s.Deps.Credentials.Create(f.scoped, f.org, credentials.CreateInput{Name: "login", Kind: credentials.KindSSHPassword, Username: "root", Material: credentials.Material{Password: markers[1]}}); err != nil {
		t.Fatal(err)
	}
	gitTok, err := f.s.Deps.Credentials.Create(f.scoped, f.org, credentials.CreateInput{Name: "git", Kind: credentials.KindGitToken, Username: "x", Material: credentials.Material{Token: markers[2]}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := f.s.Deps.Inventory.GetGroup(f.scoped, f.org, f.group)
	if err != nil {
		t.Fatal(err)
	}
	g, _, err = f.s.Deps.Inventory.SetSchema(f.scoped, f.org, f.group, g.Revision, schema.Schema{Fields: []schema.Field{{Name: "db_password", Type: schema.TypeSecret}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Inventory.SetVars(f.scoped, f.org, f.group, g.Revision, map[string]any{"db_password": markers[3]}); err != nil {
		t.Fatal(err)
	}
	cur, err := f.s.Deps.Templates.Get(f.scoped, f.org, f.template)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Templates.Update(f.scoped, f.org, f.template, templates.UpdateInput{
		Name: cur.Name, PlaybookID: cur.PlaybookID, Scope: cur.Scope, CredentialID: &gitTok.ID,
		ExtraVarsSchema:  &schema.Schema{Fields: []schema.Field{{Name: "api_key", Type: schema.TypeSecret}}},
		ExtraVars:        map[string]any{"api_key": markers[4]},
		VaultCredentials: []templates.VaultCredential{{Label: "noidee", CredentialID: f.vault}},
	}); err != nil {
		// A git token is not an SSH credential; fall back to no credential.
		if _, err := f.s.Deps.Templates.Update(f.scoped, f.org, f.template, templates.UpdateInput{
			Name: cur.Name, PlaybookID: cur.PlaybookID, Scope: cur.Scope,
			ExtraVarsSchema:  &schema.Schema{Fields: []schema.Field{{Name: "api_key", Type: schema.TypeSecret}}},
			ExtraVars:        map[string]any{"api_key": markers[4]},
			VaultCredentials: []templates.VaultCredential{{Label: "noidee", CredentialID: f.vault}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	job := f.startJob(t, []jobs.Event{{Seq: 1, Host: "web-1", Task: "t", State: "ok", Payload: payload("hello")}})

	cs := mustConnect(t, f.s, f.token)
	var req struct {
		RequestID string `json:"request_id"`
	}
	callOK(t, cs, "request_run", map[string]any{"template_id": f.template.String(), "reason": "sweep"}, &req)

	ids := map[string]any{
		"host_id": f.host.String(), "group_id": f.group.String(), "repo_id": f.repo.String(), "playbook_id": f.playbook.String(),
		"template_id": f.template.String(), "job_id": job.String(), "request_id": req.RequestID,
	}
	args := map[string]map[string]any{
		"update_host":         {"host_id": f.host.String(), "fields": map[string]any{"monitored": false}},
		"update_group":        {"group_id": f.group.String(), "fields": map[string]any{"description": "d"}},
		"set_group_vars":      {"group_id": f.group.String(), "vars": map[string]any{"db_password": map[string]any{"$secret": "keep"}}},
		"update_repo":         {"repo_id": f.repo.String(), "fields": map[string]any{"branch": "main"}},
		"update_job_template": {"template_id": f.template.String(), "fields": map[string]any{"limit": "web-1"}},
		"add_group_member":    {"group_id": f.group.String(), "host_id": f.host.String()},
		"request_retry":       {"job_id": job.String(), "reason": "sweep"},
	}
	names := toolNames(t, cs)
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	called := 0
	for _, name := range sorted {
		if strings.HasPrefix(name, "create_") || name == "remove_group_member" || name == "cancel_job" || name == "sync_repo" || name == "set_group_schema" || name == "set_host_ssh" || name == "request_run" {
			continue // they create or change things without reading the seeded secrets back beyond what is covered
		}
		a, ok := args[name]
		if !ok {
			a = ids
		}
		text, _ := call(t, cs, name, a)
		called++
		for _, m := range markers {
			if strings.Contains(text, m) {
				t.Errorf("%s output contains the secret %q:\n%s", name, m, text)
			}
		}
	}
	if called < 20 {
		t.Fatalf("only %d tools swept", called)
	}
}
