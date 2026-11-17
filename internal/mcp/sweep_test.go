package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/checks"
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
	markers := []string{"vault-passphrase-7f3a", "sweep-ssh-password-91c2", "sweep-git-token-55aa", "sweep-group-secret-3e1f", "sweep-template-secret-8b0d", "sweep-org-secret-c4d2", "sweep-member-secret-a9e7"}

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
	g, _, err = f.s.Deps.Inventory.SetSchema(f.scoped, f.org, f.group, g.Revision, schema.Schema{Fields: []schema.Field{
		{Name: "db_password", Type: schema.TypeSecret},
		{Name: "member_token", Type: schema.TypeSecret, Scope: schema.ScopeHost},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Inventory.SetVars(f.scoped, f.org, f.group, g.Revision, map[string]any{"db_password": markers[3]}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Inventory.Assign(f.scoped, f.org, f.group, f.host, map[string]any{"member_token": markers[6]}, nil); err != nil {
		t.Fatal(err)
	}
	// An org secret, referenced by an HTTP check's secret parameter on the host.
	if _, err := f.s.Deps.OrgSecrets.Create(f.scoped, f.org, "sweep-api", map[string]string{"token": markers[5]}); err != nil {
		t.Fatal(err)
	}
	check, err := f.s.Checks.CreateDefinition(f.scoped, f.org, checks.DefinitionInput{
		Key: "web-http", Plugin: "http", Params: map[string]any{"url": "http://10.0.0.1/", "secret": "sweep-api"},
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingHost, HostID: &f.host,
	})
	if err != nil {
		t.Fatal(err)
	}
	alert := f.s.SeedOpenAlertWithDeferredNotification(t, f.org)
	now := time.Now().UTC()
	var win struct {
		ID     string `json:"id"`
		Window struct {
			ID string `json:"id"`
		} `json:"window"`
	}
	if err := json.Unmarshal(f.ownerJSON(t, http.MethodPost, "/maintenance-windows", map[string]any{
		"name": "sweep window", "target_kind": "host", "target_id": f.host.String(),
		"starts_at": now.Add(time.Hour).Format(time.RFC3339), "ends_at": now.Add(2 * time.Hour).Format(time.RFC3339),
	}), &win); err != nil {
		t.Fatal(err)
	}
	var grp struct {
		ID    string `json:"id"`
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	if err := json.Unmarshal(f.ownerJSON(t, http.MethodPost, "/monitoring-groups", map[string]any{"name": "sweep-tier", "selector": "role=web"}), &grp); err != nil {
		t.Fatal(err)
	}
	scheduleID := f.seedSchedule(t)
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
		"check_id": check.ID.String(), "alert_id": alert.String(), "window_id": firstNonEmpty(win.ID, win.Window.ID), "monitoring_group_id": firstNonEmpty(grp.ID, grp.Group.ID),
		"schedule_id": scheduleID,
	}
	var rules struct {
		Rules []struct {
			ID string `json:"id"`
		} `json:"rules"`
	}
	callOK(t, cs, "list_alert_rules", nil, &rules)
	if len(rules.Rules) == 0 {
		t.Fatal("no alert rules seeded")
	}
	ids["rule_id"] = rules.Rules[0].ID
	args := map[string]map[string]any{
		"update_host":         {"host_id": f.host.String(), "fields": map[string]any{"monitored": false}},
		"update_group":        {"group_id": f.group.String(), "fields": map[string]any{"description": "d"}},
		"set_group_vars":      {"group_id": f.group.String(), "vars": map[string]any{"db_password": map[string]any{"$secret": "keep"}}},
		"update_repo":         {"repo_id": f.repo.String(), "fields": map[string]any{"branch": "main"}},
		"update_job_template": {"template_id": f.template.String(), "fields": map[string]any{"limit": "web-1"}},
		"add_group_member":    {"group_id": f.group.String(), "host_id": f.host.String()},
		"request_retry":       {"job_id": job.String(), "reason": "sweep"},
		"set_host_tags":       {"host_id": f.host.String(), "tags": []map[string]any{{"key": "env", "value": "prod"}}},
		"snooze_alert":        {"alert_id": alert.String(), "until": now.Add(time.Hour).Format(time.RFC3339)},
		"get_calendar":        {"from": now.Format(time.RFC3339), "to": now.Add(24 * time.Hour).Format(time.RFC3339)},
		"list_activity":       {"limit": 100},
		"get_host_metrics":    {"host_id": f.host.String(), "check": "http", "range": "1h"},
	}
	// The monitoring, schedule and activity tools answer for the seeded data.
	mustSucceed := map[string]bool{}
	for _, n := range append(append([]string{}, monitoringReadTools...), "list_schedules", "get_schedule", "list_schedule_runs", "get_calendar", "list_activity", "set_host_tags", "start_connection_test") {
		mustSucceed[n] = true
	}
	for _, n := range []string{"get_host_agent", "get_host_metrics"} {
		delete(mustSucceed, n) // a host without an agent or samples answers with a refusal
	}
	names := toolNames(t, cs)
	props := toolProperties(t, cs)
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	// The sweep reaches the data: the check shows the secret's name, never its value.
	if text := callOK(t, cs, "get_check", map[string]any{"check_id": check.ID.String()}, nil); !strings.Contains(text, "sweep-api") {
		t.Fatalf("get_check does not show the check's secret reference:\n%s", text)
	}
	called := 0
	swept := map[string]bool{}
	for _, name := range sorted {
		if strings.HasPrefix(name, "create_") || name == "remove_group_member" || name == "cancel_job" || name == "sync_repo" || name == "set_group_schema" || name == "set_host_ssh" || name == "request_run" {
			continue // they create or change things without reading the seeded secrets back beyond what is covered
		}
		a, ok := args[name]
		if !ok {
			a = argsFor(props[name], ids)
		}
		text, isErr := call(t, cs, name, a)
		if isErr && mustSucceed[name] {
			t.Errorf("%s failed with the seeded arguments: %s", name, text)
		}
		called++
		swept[name] = true
		for _, m := range markers {
			if strings.Contains(text, m) {
				t.Errorf("%s output contains the secret %q:\n%s", name, m, text)
			}
		}
	}
	for name := range mustSucceed {
		if !swept[name] {
			t.Errorf("%s was not swept", name)
		}
	}
	if called < 40 {
		t.Fatalf("only %d tools swept", called)
	}
}

// toolProperties maps each listed tool to the argument names its input
// schema declares; the schemas refuse any other property.
func toolProperties(t *testing.T, cs *sdk.ClientSession) map[string]map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]bool{}
	for _, tl := range res.Tools {
		raw, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var sch struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &sch); err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for k := range sch.Properties {
			m[k] = true
		}
		out[tl.Name] = m
	}
	return out
}

// argsFor picks the values of ids a tool's schema declares.
func argsFor(declared map[string]bool, ids map[string]any) map[string]any {
	a := map[string]any{}
	for k, v := range ids {
		if declared[k] {
			a[k] = v
		}
	}
	return a
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
