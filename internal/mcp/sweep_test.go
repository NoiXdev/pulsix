package mcp_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/ssh"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/credentials"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/repos"
	"github.com/noixdev/pulsix/internal/templates"
)

// TestNoToolOutputContainsASecret seeds secrets of every kind the tools sit
// next to, calls every tool with arguments that reach that data, and
// asserts no output carries any of them (Review Focus 1).
func TestNoToolOutputContainsASecret(t *testing.T) {
	f := newOrgFixture(t)
	// The SSH key's markers are its PEM armour and the first line of its
	// base64 body, which is what a leaked key would show.
	keyPEM, pemBody := sweepPEM(t)
	markers := []string{"vault-passphrase-7f3a", "sweep-ssh-password-91c2", "sweep-git-token-55aa", "sweep-group-secret-3e1f", "sweep-template-secret-8b0d", "sweep-org-secret-c4d2", "sweep-member-secret-a9e7", "OPENSSH PRIVATE KEY", pemBody}

	if _, err := f.s.Deps.Credentials.Create(f.scoped, f.org, credentials.CreateInput{Name: "login", Kind: credentials.KindSSHPassword, Username: "root", Material: credentials.Material{Password: markers[1]}}); err != nil {
		t.Fatal(err)
	}
	pemKey, err := f.s.Deps.Credentials.Create(f.scoped, f.org, credentials.CreateInput{Name: "pem-key", Kind: credentials.KindSSHKey, Username: "ansible", Material: credentials.Material{PrivateKey: keyPEM}})
	if err != nil {
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
	if err := f.s.Deps.Hosts.RecordConnectionTest(f.scoped, f.org, f.host, time.Now(), true, "SHA256:sweepfingerprint", "", "unpinned",
		[]hosts.HostTestCheck{{Name: "login", Detail: "ok", OK: true}}, nil); err != nil {
		t.Fatal(err)
	}
	obsAt := time.Now()
	v := 12.5
	if _, err := f.s.Checks.RecordObservations(f.scoped, f.org, []checks.Observation{{HostID: f.host, CheckKey: "cpu", Metric: "usage_percent", At: obsAt.Add(-time.Minute), Value: &v}}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Deps.Agents.Register(f.scoped, agents.Registration{
		OrgID: f.org, HostID: f.host, AgentName: "web-1", Version: "v1", Serial: "serial-sweep",
		NotBefore: obsAt.Add(-time.Hour), NotAfter: obsAt.Add(time.Hour),
	}); err != nil {
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
	// The template carries its secrets before the schedule is created, so the
	// schedule is the one the sweep reads them through.
	cur, err := f.s.Deps.Templates.Get(f.scoped, f.org, f.template)
	if err != nil {
		t.Fatal(err)
	}
	// The template runs with the PEM key and the vault passphrase credential.
	if _, err := f.s.Deps.Templates.Update(f.scoped, f.org, f.template, templates.UpdateInput{
		Name: cur.Name, PlaybookID: cur.PlaybookID, Scope: cur.Scope, CredentialID: &pemKey.ID,
		ExtraVarsSchema:  &schema.Schema{Fields: []schema.Field{{Name: "api_key", Type: schema.TypeSecret}}},
		ExtraVars:        map[string]any{"api_key": markers[4]},
		VaultCredentials: []templates.VaultCredential{{Label: "noidee", CredentialID: f.vault}},
	}); err != nil {
		t.Fatal(err)
	}
	scheduleID := f.seedSchedule(t)
	curRepo, err := f.s.Deps.Repos.Get(f.scoped, f.org, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Deps.Repos.Update(f.scoped, f.org, f.repo, repos.UpdateInput{
		Name: curRepo.Name, URL: curRepo.URL, Branch: curRepo.Branch, Subpath: curRepo.Subpath, CredentialID: &gitTok.ID,
	}); err != nil {
		t.Fatal(err)
	}
	failed := f.startFailedJob(t)
	job := f.startJob(t, []jobs.Event{{Seq: 1, Host: "web-1", Task: "t", State: "ok", Payload: payload("hello")}})

	cs := mustConnect(t, f.s, f.token)
	var req struct {
		RequestID string `json:"request_id"`
	}
	checkClean := func(name, text string) {
		t.Helper()
		for _, m := range markers {
			if strings.Contains(text, m) {
				t.Errorf("%s output contains the secret %q:\n%s", name, m, text)
			}
		}
	}
	checkClean("request_run", callOK(t, cs, "request_run", map[string]any{"template_id": f.template.String(), "reason": "sweep"}, &req))

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
	checkClean("list_alert_rules", callOK(t, cs, "list_alert_rules", nil, &rules))
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
		"request_retry":       {"job_id": failed.String(), "reason": "sweep"},
		"set_host_tags":       {"host_id": f.host.String(), "tags": []map[string]any{{"key": "env", "value": "prod"}}},
		"snooze_alert":        {"alert_id": alert.String(), "until": now.Add(time.Hour).Format(time.RFC3339)},
		"get_calendar":        {"from": now.Format(time.RFC3339), "to": now.Add(24 * time.Hour).Format(time.RFC3339)},
		"list_activity":       {"limit": 100},
		"get_host_metrics":    {"host_id": f.host.String(), "check": "cpu", "metric": "usage_percent", "range": "1h"},
	}
	// Every swept tool must succeed with the seeded arguments, so an erroring
	// tool cannot pass for "no secret in the output".
	names := toolNames(t, cs)
	props := toolProperties(t, cs)
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	// The sweep reaches the data: the check shows the secret's name, never its value.
	skipped := map[string]string{
		"remove_group_member": "would delete the membership whose host-scoped secret the sweep reads back in get_group and render_inventory",
		"cancel_job":          "would end the seeded job that get_job, get_job_log and get_run_request read",
		"sync_repo":           "starts a git clone job that needs a reachable remote; the repository's credential shows in get_repo, which is swept",
		"set_group_schema":    "replaces the schema that types the seeded secrets, which set_group_vars is swept against",
		"set_host_ssh":        "rewires the host's SSH credential; the same view is read by get_host, which is swept",
		"request_run":         "called above as setup, and its output is checked there",
	}
	if text := callOK(t, cs, "get_check", map[string]any{"check_id": check.ID.String()}, nil); !strings.Contains(text, "sweep-api") {
		t.Fatalf("get_check does not show the check's secret reference:\n%s", text)
	}
	called := 0
	swept := map[string]bool{}
	outputs := map[string]string{}
	for _, name := range sorted {
		if strings.HasPrefix(name, "create_") {
			continue // takes new content as arguments and answers with the object the matching get_ tool shows, which is swept
		}
		if _, skip := skipped[name]; skip {
			continue
		}
		a, ok := args[name]
		if !ok {
			a = argsFor(props[name], ids)
		}
		text, isErr := call(t, cs, name, a)
		if isErr {
			t.Errorf("%s failed with the seeded arguments: %s", name, text)
		}
		called++
		swept[name] = true
		outputs[name] = text
		checkClean(name, text)
	}
	for name := range skipped {
		if !names[name] {
			t.Errorf("skipped tool %s is not listed any more", name)
		}
	}
	// A marker no tool could return proves nothing: each secret's owner
	// shows up in a swept output, by name or id, in masked form.
	reach := []struct{ tool, want, what string }{
		{"get_job_template", pemKey.ID.String(), "the SSH key credential"},
		{"get_job_template", f.vault.String(), "the vault passphrase credential"},
		{"get_job_template", "api_key", "the template's secret extra var"},
		{"get_repo", gitTok.ID.String(), "the repo's git credential"},
		{"get_run_request", pemKey.ID.String(), "the SSH credential in the run picture"},
		{"get_run_request", f.vault.String(), "the vault credential in the run picture"},
		// The fixture's playbook is an editor playbook with no repository, so
		// the picture carries no git credential here; get_repo above is the
		// path that shows it.
		{"get_group", "member_token", "the host-scoped membership secret"},
		{"get_group", "db_password", "the group secret"},
	}
	for _, r := range reach {
		if !strings.Contains(outputs[r.tool], r.want) {
			t.Errorf("%s output does not mention %s (%q), so its marker is not exercised:\n%s", r.tool, r.what, r.want, outputs[r.tool])
		}
	}
	// request_retry succeeded for the failed run seeded above and answered
	// with a pending request, not a refusal that happens to be clean.
	var retry struct {
		RequestID string `json:"request_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal([]byte(outputs["request_retry"]), &retry); err != nil || retry.RequestID == "" || retry.Status != "pending" {
		t.Errorf("request_retry output = %q (%v), want a pending request", outputs["request_retry"], err)
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

// sweepPEM returns an OpenSSH private key and a line from the middle of its
// base64 body, a string no output may carry. The body's first line is the
// same for every unencrypted ed25519 key, so it would not catch a leak of the
// key material itself.
func sweepPEM(t *testing.T) (pemText, bodyLine string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	pemText = string(pem.EncodeToMemory(block))
	lines := strings.Split(pemText, "\n")
	return pemText, lines[len(lines)/2]
}
