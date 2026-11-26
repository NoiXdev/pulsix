// Phase 3 of the assistant suite: every tool the all-areas token lists is
// called with valid arguments. The writes run first, each checked through
// the owner's REST session; the reads follow, each expected to show the
// seeded object it names.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type idOut struct {
	ID string `json:"id"`
}

func (a *assistantSuite) everyTool() {
	s := a.s
	s.t.Helper()
	cs := a.mustConnect(a.allToken)
	ids := a.inventoryWrites(cs)
	a.repoAndTemplateWrites(cs)
	a.alertWrites(cs)
	a.automationWrites(cs)

	// --- Job tools: a pending request and two refusals --------------------
	// request_retry and cancel_job succeed only on a run of the token's own
	// approved request, which the run_request phase makes and asserts.
	// Here they are called where they must be refused.
	var req requestedRun
	a.recordOK(cs, "request_run", map[string]any{"template_id": a.seeded.templateA, "reason": "e2e every tool"}, &req)
	ids["request_id"] = req.RequestID
	text, isErr := a.record(cs, "request_retry", map[string]any{"job_id": ids["job_id"], "reason": "e2e every tool"})
	if want := retryOfNoRunRefusal; !isErr || !strings.Contains(text, want) {
		s.t.Errorf("request_retry of the connection test job = %v %s, want refused with %q", isErr, text, want)
	}
	a.cancelOfOthersRunRefused(cs)

	// --- Every other tool: the reads --------------------------------------
	var rules struct {
		Rules []idOut `json:"rules"`
	}
	a.recordOK(cs, "list_alert_rules", nil, &rules)
	if len(rules.Rules) == 0 {
		s.t.Fatal("list_alert_rules shows no rule")
	}
	ids["rule_id"] = rules.Rules[0].ID
	a.reads(cs, ids)

	if got, want := sortedCopy(keys(a.called)), sortedCopy(a.allTools); !slices.Equal(got, want) {
		s.t.Fatalf("called tools differ from the all-areas list\nnot called: %v\nnot listed: %v", minus(want, got), minus(got, want))
	}
}

// The refusals of request_retry for a job that is no playbook run, and of
// cancel_job for a run the token did not request: without jobs.run (in no
// area) only the token's own requests' runs may be cancelled.
const (
	retryOfNoRunRefusal   = "only run jobs can be retried"
	cancelOfOthersRefusal = "the token's areas do not include jobs.run"
)

// cancelOfOthersRunRefused starts a blocking run in the owner's session,
// has the token's cancel_job on it refused, and cancels it as the owner.
func (a *assistantSuite) cancelOfOthersRunRefused(cs *sdk.ClientSession) {
	s := a.s
	s.t.Helper()
	release := filepath.Join(s.t.TempDir(), "release")
	s.t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	tid := s.createTemplate("asst-owner-block", a.seeded.playbookA, map[string]any{"greeting": s.greeting, "block_until": release})
	job := s.runTemplate(tid)
	s.awaitJobRunning(job, chainWait)
	text, isErr := a.record(cs, "cancel_job", map[string]any{"job_id": job})
	if !isErr || !strings.Contains(text, cancelOfOthersRefusal) {
		s.t.Errorf("cancel_job of the owner's running job = %v %s, want refused with %q", isErr, text, cancelOfOthersRefusal)
	}
	var running jobDetailOut
	s.get("/api/v1/"+s.apiPath()+"/jobs/"+job, &running)
	if running.Job.Status != "running" {
		s.t.Errorf("the owner's job after the refused cancel_job = %q, want still running", running.Job.Status)
	}
	s.post("/api/v1/"+s.apiPath()+"/jobs/"+job+"/cancel", struct{}{}, nil)
	if done := s.awaitJob(job, "owner's blocking run", chainWait); done.Job.Status != "cancelled" {
		s.t.Fatalf("the owner's blocking run after its cancel = %q, want cancelled", done.Job.Status)
	}
}

// inventoryWrites drives the inventory write tools and returns the ids the
// reads use.
func (a *assistantSuite) inventoryWrites(cs *sdk.ClientSession) map[string]any {
	s := a.s
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	var host struct {
		Host idOut `json:"host"`
	}
	a.recordOK(cs, "create_host", map[string]any{"name": "e2e-asst-h2", "zone_id": s.zoneID, "address": "192.0.2.21", "managed": true}, &host)
	h2 := host.Host.ID
	a.recordOK(cs, "update_host", map[string]any{"host_id": h2, "fields": map[string]any{"address": "192.0.2.22"}}, nil)
	var hostRead struct {
		Host struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"host"`
	}
	s.get(prefix+"/hosts/"+h2, &hostRead)
	if hostRead.Host.Name != "e2e-asst-h2" || hostRead.Host.Address != "192.0.2.22" {
		s.t.Fatalf("created and updated host = %+v, want e2e-asst-h2 at 192.0.2.22", hostRead.Host)
	}
	a.recordOK(cs, "set_host_ssh", map[string]any{"host_id": h2, "username": "deploy", "port": 2222, "credential_id": a.seeded.importedKeyCred}, nil)
	var ssh struct {
		SSH struct {
			Username     string  `json:"username"`
			Port         int     `json:"port"`
			CredentialID *string `json:"credential_id"`
		} `json:"ssh"`
	}
	s.get(prefix+"/hosts/"+h2+"/ssh", &ssh)
	if ssh.SSH.Username != "deploy" || ssh.SSH.Port != 2222 || ssh.SSH.CredentialID == nil || *ssh.SSH.CredentialID != a.seeded.importedKeyCred {
		s.t.Fatalf("host ssh after set_host_ssh = %+v", ssh.SSH)
	}
	a.recordOK(cs, "set_host_tags", map[string]any{"host_id": s.hostID, "tags": []map[string]any{{"key": "env", "value": "e2e"}, {"key": "assistant"}}}, nil)
	var tags struct {
		Tags []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"tags"`
	}
	s.get(prefix+"/hosts/"+s.hostID+"/tags", &tags)
	got := map[string]string{}
	for _, tg := range tags.Tags {
		got[tg.Key] = tg.Value
	}
	if v, ok := got["env"]; !ok || v != "e2e" {
		s.t.Fatalf("host tags after set_host_tags = %+v, want env=e2e", tags.Tags)
	}
	if _, ok := got["assistant"]; !ok {
		s.t.Fatalf("host tags after set_host_tags = %+v, want the value-less tag assistant", tags.Tags)
	}
	var test struct {
		JobID string `json:"job_id"`
	}
	a.recordOK(cs, "start_connection_test", map[string]any{"host_id": s.hostID}, &test)
	if job := s.awaitJob(test.JobID, "connection_test", chainWait); job.Job.Status != "succeeded" {
		s.t.Fatalf("connection test started by the assistant = %q (error %q)", job.Job.Status, job.Job.Error)
	}

	var grp struct {
		Group idOut `json:"group"`
	}
	a.recordOK(cs, "create_group", map[string]any{"name": "e2e_asst_created"}, &grp)
	g2 := grp.Group.ID
	a.recordOK(cs, "update_group", map[string]any{"group_id": g2, "fields": map[string]any{"description": "made by the assistant"}}, nil)
	a.recordOK(cs, "set_group_schema", map[string]any{"group_id": g2, "schema": map[string]any{"fields": []map[string]any{{"name": "tier", "type": "string", "scope": "host"}}}}, nil)
	var groupRead struct {
		Group struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Schema      struct {
				Fields []struct {
					Name string `json:"name"`
				} `json:"fields"`
			} `json:"schema"`
		} `json:"group"`
	}
	s.get(prefix+"/inventory/groups/"+g2, &groupRead)
	if g := groupRead.Group; g.Name != "e2e_asst_created" || g.Description != "made by the assistant" || len(g.Schema.Fields) != 1 || g.Schema.Fields[0].Name != "tier" {
		s.t.Fatalf("created group = %+v, want e2e_asst_created with its description and the tier field", g)
	}
	a.recordOK(cs, "add_group_member", map[string]any{"group_id": g2, "host_id": h2, "vars": map[string]any{"tier": "web"}}, nil)
	if !a.isMember(g2, h2) {
		s.t.Fatal("add_group_member: the host is not a member through REST")
	}
	var members struct {
		Members []struct {
			HostID   string         `json:"host_id"`
			Revision int64          `json:"revision"`
			Vars     map[string]any `json:"vars"`
		} `json:"members"`
	}
	s.get(prefix+"/inventory/groups/"+g2+"/members", &members)
	if len(members.Members) != 1 || members.Members[0].HostID != h2 {
		s.t.Fatalf("members of the created group = %+v, want only %s", members.Members, h2)
	}
	a.recordOK(cs, "set_member_vars", map[string]any{"group_id": g2, "host_id": h2, "revision": members.Members[0].Revision, "vars": map[string]any{"tier": "db"}}, nil)
	members.Members = nil
	s.get(prefix+"/inventory/groups/"+g2+"/members", &members)
	if len(members.Members) != 1 || members.Members[0].Vars["tier"] != "db" {
		s.t.Fatalf("members after set_member_vars = %+v, want tier db", members.Members)
	}
	a.recordOK(cs, "remove_group_member", map[string]any{"group_id": g2, "host_id": h2}, nil)
	if a.isMember(g2, h2) {
		s.t.Fatal("remove_group_member: the host is still a member through REST")
	}
	a.recordOK(cs, "set_group_vars", map[string]any{"group_id": a.seeded.secretGroupID, "vars": map[string]any{"db_password": map[string]any{"$secret": "keep"}}}, nil)
	var secretGroup struct {
		Group struct {
			Vars map[string]any `json:"vars"`
		} `json:"group"`
	}
	s.get(prefix+"/inventory/groups/"+a.seeded.secretGroupID, &secretGroup)
	if v, _ := secretGroup.Group.Vars["db_password"].(map[string]any); v["$secret"] != "set" {
		s.t.Fatalf("secret group vars after keep = %v, want db_password still set", secretGroup.Group.Vars)
	}

	return map[string]any{
		"host_id": s.hostID, "group_id": a.seeded.secretGroupID, "repo_id": s.repoID, "playbook_id": a.seeded.playbookA,
		"template_id": a.seeded.templateA, "job_id": test.JobID, "check_id": a.seeded.checkID, "alert_id": a.seeded.alertID,
		"window_id": a.seeded.windowID, "monitoring_group_id": a.seeded.monitoringGroupID, "schedule_id": a.seeded.scheduleID,
	}
}

// isMember reports whether host is a member of group, through REST.
func (a *assistantSuite) isMember(group, host string) bool {
	a.s.t.Helper()
	var out struct {
		Members []struct {
			HostID string `json:"host_id"`
		} `json:"members"`
	}
	a.s.get("/api/v1/"+a.s.apiPath()+"/inventory/groups/"+group+"/members", &out)
	for _, m := range out.Members {
		if m.HostID == host {
			return true
		}
	}
	return false
}

// repoAndTemplateWrites drives the repository and job template writes.
func (a *assistantSuite) repoAndTemplateWrites(cs *sdk.ClientSession) {
	s := a.s
	s.t.Helper()
	prefix := "/api/v1/" + s.apiPath()
	var repo struct {
		Repo idOut `json:"repo"`
	}
	a.recordOK(cs, "create_repo", map[string]any{"name": "e2e-asst-repo", "url": "ssh://git@192.0.2.30/infra.git", "branch": "main"}, &repo)
	a.recordOK(cs, "update_repo", map[string]any{"repo_id": repo.Repo.ID, "fields": map[string]any{"branch": "develop"}}, nil)
	var repoRead struct {
		Repo struct {
			Name   string `json:"name"`
			Branch string `json:"branch"`
		} `json:"repo"`
	}
	s.get(prefix+"/repos/"+repo.Repo.ID, &repoRead)
	if repoRead.Repo.Name != "e2e-asst-repo" || repoRead.Repo.Branch != "develop" {
		s.t.Fatalf("created and updated repo = %+v, want e2e-asst-repo on develop", repoRead.Repo)
	}
	var sync struct {
		Job idOut `json:"job"`
	}
	a.recordOK(cs, "sync_repo", map[string]any{"repo_id": s.repoID}, &sync)
	if job := s.awaitJob(sync.Job.ID, "sync_repo", chainWait); job.Job.Status != "succeeded" {
		s.t.Fatalf("repo sync started by the assistant = %q (error %q)", job.Job.Status, job.Job.Error)
	}
	var pb struct {
		Playbook idOut `json:"playbook"`
	}
	a.recordOK(cs, "create_playbook", map[string]any{"repo_id": s.repoID, "path": "hello.yml", "name": "e2e-asst-hello"}, &pb)
	var tmpl struct {
		Template idOut `json:"template"`
	}
	a.recordOK(cs, "create_job_template", map[string]any{
		"name": "e2e-asst-created", "playbook_id": pb.Playbook.ID, "scope": map[string]any{"host_ids": []string{s.hostID}},
		"vault_identities": []map[string]any{{"label": "asst", "credential_id": a.seeded.vaultCred}},
	}, &tmpl)
	a.recordOK(cs, "update_job_template", map[string]any{"template_id": tmpl.Template.ID, "fields": map[string]any{"limit": hostName}}, nil)
	var tmplRead struct {
		Template struct {
			Name            string `json:"name"`
			Limit           string `json:"limit"`
			VaultIdentities []struct {
				Label string `json:"label"`
			} `json:"vault_identities"`
		} `json:"template"`
	}
	s.get(prefix+"/job-templates/"+tmpl.Template.ID, &tmplRead)
	if tr := tmplRead.Template; tr.Name != "e2e-asst-created" || tr.Limit != hostName || len(tr.VaultIdentities) != 1 || tr.VaultIdentities[0].Label != "asst" {
		s.t.Fatalf("created and updated template = %+v, want e2e-asst-created limited to %s with the asst vault identity", tr, hostName)
	}
}

// alertWrites acknowledges, unacknowledges, snoozes and unsnoozes the
// seeded alert, each checked through REST. The alert ends open and
// unacknowledged, as the edit phase needs it.
func (a *assistantSuite) alertWrites(cs *sdk.ClientSession) {
	s := a.s
	s.t.Helper()
	path := "/api/v1/" + s.apiPath() + "/alerts/" + a.seeded.alertID
	type alertRead struct {
		Alert struct {
			AckAt        *time.Time `json:"ack_at"`
			AckNote      string     `json:"ack_note"`
			SnoozedUntil *time.Time `json:"snoozed_until"`
		} `json:"alert"`
	}
	read := func() alertRead {
		var out alertRead
		s.get(path, &out)
		return out
	}
	a.recordOK(cs, "acknowledge_alert", map[string]any{"alert_id": a.seeded.alertID, "note": "seen by the assistant"}, nil)
	if r := read(); r.Alert.AckAt == nil || r.Alert.AckNote != "seen by the assistant" {
		s.t.Fatalf("alert after acknowledge_alert = %+v, want acknowledged with the note", r.Alert)
	}
	a.recordOK(cs, "unacknowledge_alert", map[string]any{"alert_id": a.seeded.alertID}, nil)
	if r := read(); r.Alert.AckAt != nil {
		s.t.Fatalf("alert after unacknowledge_alert = %+v, want no acknowledgement", r.Alert)
	}
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	a.recordOK(cs, "snooze_alert", map[string]any{"alert_id": a.seeded.alertID, "until": until.Format(time.RFC3339)}, nil)
	if r := read(); r.Alert.SnoozedUntil == nil || !r.Alert.SnoozedUntil.Equal(until) {
		s.t.Fatalf("alert after snooze_alert = %+v, want snoozed until %s", r.Alert, until)
	}
	a.recordOK(cs, "unsnooze_alert", map[string]any{"alert_id": a.seeded.alertID}, nil)
	if r := read(); r.Alert.SnoozedUntil != nil {
		s.t.Fatalf("alert after unsnooze_alert = %+v, want no snooze", r.Alert)
	}
}

// reads calls every listed tool not called yet, with the arguments its
// schema declares from ids (or an explicit set), and checks that each one
// shows what its row in shows names; a tool without a row fails.
func (a *assistantSuite) reads(cs *sdk.ClientSession, ids map[string]any) {
	s := a.s
	s.t.Helper()
	now := time.Now().UTC()
	// The tools whose arguments are not just the ids their schema declares
	// (list_jobs would filter by template A, hiding the connection test job
	// its row looks for).
	explicit := map[string]map[string]any{
		"get_calendar":      {"from": now.Format(time.RFC3339), "to": now.Add(48 * time.Hour).Format(time.RFC3339)},
		"list_activity":     {"object_type": "org_settings"},
		"get_host_metrics":  {"host_id": s.hostID, "check": "cpu", "metric": cpuMetric, "range": "1h"},
		"preview_inventory": {},
		"list_jobs":         {},
	}
	shows := map[string][]string{
		"get_organization": {s.org}, "list_hosts": {hostName}, "get_host": {hostName}, "list_groups": {"asst_secrets", "e2e_asst_created"},
		"get_group": {"asst_secrets", "db_password"}, "preview_inventory": {hostName}, "list_zones": {s.zoneID},
		"get_connection_test": {"host_key"}, "list_repos": {"e2e-asst-repo"}, "get_repo": {s.repoID}, "list_repo_files": {"hello.yml"},
		"list_playbooks": {a.seeded.playbookA, "hello.yml"}, "get_playbook": {a.seeded.playbookA, "Pulsix greeting"}, "list_job_templates": {"asst-a", "asst-b", "e2e-asst-created"},
		"get_job_template": {"asst-a", a.seeded.templateB}, "list_schedules": {"asst-yearly"}, "get_schedule": {"asst-yearly", `"paused":true`},
		"get_calendar": {a.seeded.windowID}, "list_credentials": {"asst-imported", "asst-vault", "host-login"},
		"list_jobs": {ids["job_id"].(string)}, "get_job": {ids["job_id"].(string)}, "list_run_requests": {ids["request_id"].(string)},
		"get_run_request": {ids["request_id"].(string)}, "list_agents": {"asst-agent"}, "get_host_agent": {"asst-agent"},
		"list_checks": {"asst-http"}, "get_check": {"asst-http", "asst-api"}, "list_host_checks": {"asst-http"},
		"list_monitoring_groups": {"asst-tier"}, "get_monitoring_group": {"asst-tier"}, "get_alert_rule": {ids["rule_id"].(string)},
		"list_alerts": {a.seeded.alertID}, "get_alert": {a.seeded.alertID, "acknowledged", "snoozed"},
		"list_maintenance_windows": {"asst window"}, "get_maintenance_window": {"asst window"}, "list_activity": {"turned assistant access on"},
		"get_host_metrics": {cpuMetric}, "get_host_effective_rules": {"asst-host-cpu"}, "list_schedule_runs": {a.seeded.scheduledGroup},
		"get_job_log": {"connection_test"}, "get_host_vars": {"ansible_host", "sources"},
	}
	props := a.toolProperties(cs)
	for _, name := range a.allTools {
		if a.called[name] {
			continue
		}
		args, ok := explicit[name]
		if !ok {
			args = map[string]any{}
			for k, v := range ids {
				if props[name][k] {
					args[k] = v
				}
			}
		}
		text, isErr := a.record(cs, name, args)
		if isErr {
			s.t.Errorf("%s(%v) failed: %s", name, args, text)
			continue
		}
		if name == "get_host_metrics" {
			a.expectCPUSample(text)
		}
		wants, ok := shows[name]
		if !ok {
			s.t.Errorf("%s has no content check in shows:\n%s", name, text)
		}
		for _, want := range wants {
			if !strings.Contains(text, want) {
				s.t.Errorf("%s output does not show %q:\n%s", name, want, text)
			}
		}
	}
	// The filtered list_activity above proves the content; this unfiltered
	// call puts every audit entry, including those about secret-holding
	// objects, into the recorded outputs the secret sweep reads.
	if text, isErr := a.record(cs, "list_activity", map[string]any{"limit": 100}); isErr {
		s.t.Errorf("unfiltered list_activity failed: %s", text)
	}
}

// expectCPUSample checks get_host_metrics' output for exactly the one cpu
// sample the seed recorded, by its parsed value, so 112.5 or 12.55 do not
// pass for 12.5.
func (a *assistantSuite) expectCPUSample(text string) {
	s := a.s
	s.t.Helper()
	var out struct {
		Check  string `json:"check"`
		Metric string `json:"metric"`
		Points []struct {
			Value float64 `json:"value"`
		} `json:"points"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		s.t.Errorf("get_host_metrics: decode %q: %v", text, err)
		return
	}
	if out.Check != "cpu" || out.Metric != cpuMetric || len(out.Points) != 1 || out.Points[0].Value != 12.5 {
		s.t.Errorf("get_host_metrics = %+v, want the one recorded cpu/%s sample of 12.5", out, cpuMetric)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// minus returns the names of a that b does not hold.
func minus(a, b []string) []string {
	var out []string
	for _, n := range a {
		if !slices.Contains(b, n) {
			out = append(out, n)
		}
	}
	return out
}
