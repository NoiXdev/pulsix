package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/templates"
)

// overridesTemplate gives the fixture's template both hosts and an extra
// vars schema with a plain and a secret field.
func (f runRequestFixture) overridesTemplate(t *testing.T) {
	t.Helper()
	updateTemplate(t, f, func(in *templates.UpdateInput) {
		in.Scope = templates.Scope{HostIDs: []uuid.UUID{f.hostA, f.hostB}}
		in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{
			{Name: "version", Type: schema.TypeString},
			{Name: "note", Type: schema.TypeString},
			{Name: "big", Type: schema.TypeFloat},
			{Name: "api_key", Type: schema.TypeSecret},
		}}
		in.ExtraVars = map[string]any{"version": "1.0", "api_key": "tmpl-s3cret-77aa"}
	})
}

func (f runRequestFixture) tryRequest(t *testing.T, body map[string]any) *apitest.Response {
	t.Helper()
	if _, ok := body["reason"]; !ok {
		body["reason"] = "roll out"
	}
	return f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/job-templates/"+f.templateID.String()+"/run-requests", "", body)
}

func TestRunRequestOverridesAreValidated(t *testing.T) {
	f := newRunRequestFixture(t, "rr-ov-valid")
	f.overridesTemplate(t)
	foreign := uuid.Must(uuid.NewV7())
	for _, tc := range []struct {
		name string
		body map[string]any
		msg  string
	}{
		{"secret extra var", map[string]any{"extra_vars": map[string]any{"api_key": "x"}}, "api_key"},
		{"host outside the template", map[string]any{"host_ids": []string{f.hostA.String(), foreign.String()}}, foreign.String()},
		{"host_id and host_ids", map[string]any{"host_id": f.hostA.String(), "host_ids": []string{f.hostA.String()}}, "host_ids"},
		{"diff without check mode", map[string]any{"diff": true}, "diff"},
		{"expiry too short", map[string]any{"expires_in": 4}, "expires_in"},
		{"expiry too long", map[string]any{"expires_in": 1441}, "expires_in"},
		{"expiry zero", map[string]any{"expires_in": 0}, "expires_in"},
		{"extra var of the wrong type", map[string]any{"extra_vars": map[string]any{"version": 3}}, "version"},
		{"limit reading a worker file", map[string]any{"limit": "@/etc/passwd"}, "limit"},
		{"limit as a regex", map[string]any{"limit": "~.*"}, "limit"},
		{"limit with a subscript", map[string]any{"limit": "web[0]"}, "limit"},
		{"limit with a ? glob", map[string]any{"limit": "web-?"}, "only host names and * wildcards"},
		{"limit matching no target", map[string]any{"limit": "db-*"}, "no host"},
		{"vault marker in a var", map[string]any{"extra_vars": map[string]any{"version": map[string]any{"__ansible_vault": "$ANSIBLE_VAULT;1.1;AES256"}}}, "version"},
		{"secret marker as a value", map[string]any{"extra_vars": map[string]any{"version": map[string]any{"$secret": "set"}}}, "version"},
		{"extra vars not an object", map[string]any{"extra_vars": []string{"x"}}, "extra_vars"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.tryRequest(t, tc.body)
			if resp.Code != http.StatusUnprocessableEntity || !strings.Contains(resp.Body.String(), tc.msg) {
				t.Fatalf("%s = %d %s, want 422 naming %q", tc.name, resp.Code, resp.Body, tc.msg)
			}
			// A refusal never echoes the refused value.
			for _, v := range []string{"/etc/passwd", "ANSIBLE_VAULT"} {
				if strings.Contains(resp.Body.String(), v) {
					t.Fatalf("%s echoes %q: %s", tc.name, v, resp.Body)
				}
			}
		})
	}
}

// TestRunRequestRefusesNamesAnsibleActsOn: on a template without a schema,
// where no declared-names rule applies, a request still cannot set a
// variable Ansible or Pulsix acts on; Ansible would run an interpreter
// override whatever unsafe marker its value carries.
func TestRunRequestRefusesNamesAnsibleActsOn(t *testing.T) {
	f := newRunRequestFixture(t, "rr-ov-names")
	for _, name := range []string{"ansible_python_interpreter", "ANSIBLE_BECOME_EXE", "hostvars", "environment", "pulsix_agent_enroll_token"} {
		resp := f.tryRequest(t, map[string]any{"extra_vars": map[string]any{name: "sh -c id #"}})
		if resp.Code != http.StatusUnprocessableEntity || !strings.Contains(resp.Body.String(), name) || !strings.Contains(resp.Body.String(), "reserved") {
			t.Fatalf("%s = %d %s, want 422 naming it as reserved", name, resp.Code, resp.Body)
		}
	}
	// The template declares no extra vars, so even a plain one is refused
	// (final review I2): nothing tells which name a secret hides under.
	if resp := f.tryRequest(t, map[string]any{"extra_vars": map[string]any{"release": "1.2"}}); resp.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(resp.Body.String(), "declares no extra variables") {
		t.Fatalf("plain var on a template without a schema = %d %s", resp.Code, resp.Body)
	}
}

func TestRunRequestRetryTakesNoOverrides(t *testing.T) {
	f := newRunRequestFixture(t, "rr-ov-retry")
	resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/jobs/"+uuid.Must(uuid.NewV7()).String()+"/retry-requests", "",
		map[string]any{"reason": "again", "check_mode": true})
	// The retry route has no override fields: they are refused, not ignored.
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("retry with check_mode = %d %s, want 422", resp.Code, resp.Body)
	}
}

// TestRunRequestDryRunWithOverridesRunsAsPictured: a request with every
// override shows them in its picture, keeps its own expiry, and its
// approval enqueues one check-mode job on the narrowed hosts that a worker
// claims with the vars and diff the person approved. The vars arrive
// marked unsafe for Ansible exactly as a webhook's do, so a template
// expression in them stays text; numbers keep every digit; the request's
// limit is spent on the hosts and never reaches Ansible.
func TestRunRequestDryRunWithOverridesRunsAsPictured(t *testing.T) {
	clock := newTestClock()
	f := newRunRequestFixture(t, "rr-ov-run", apitest.WithRunRequestClock(clock.Now))
	f.overridesTemplate(t)
	created := f.tryRequest(t, map[string]any{
		"check_mode": true, "host_ids": []string{f.hostA.String(), f.hostB.String()}, "limit": "web-*:!web-2", "expires_in": 15,
		"extra_vars": json.RawMessage(`{"version":"1.2","note":"{{ api_key }}","big":9007199254740993}`),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var out struct {
		Request struct {
			ID        string    `json:"id"`
			ExpiresAt time.Time `json:"expires_at"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"request"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if got := out.Request.ExpiresAt.Sub(out.Request.CreatedAt); got != 15*time.Minute {
		t.Fatalf("expires after %s, want 15m", got)
	}
	if strings.Contains(created.Body.String(), "tmpl-s3cret-77aa") {
		t.Fatalf("the template's secret reached the response: %s", created.Body)
	}

	d := f.detail(t, out.Request.ID)
	var picture struct {
		CheckMode         bool           `json:"check_mode"`
		Diff              bool           `json:"diff"`
		LimitOverride     string         `json:"limit_override"`
		ExtraVarsOverride map[string]any `json:"extra_vars_override"`
		RequestedHostIDs  []string       `json:"requested_host_ids"`
		Targets           []struct {
			ID string `json:"id"`
		} `json:"targets"`
		FollowUps json.RawMessage `json:"follow_ups"`
	}
	if err := json.Unmarshal(d.Picture, &picture); err != nil {
		t.Fatal(err)
	}
	// The limit drops web-2 from the requested hosts: the targets are
	// exactly the hosts that run.
	if !picture.CheckMode || !picture.Diff || picture.LimitOverride != "web-*:!web-2" || picture.ExtraVarsOverride["version"] != "1.2" ||
		len(picture.RequestedHostIDs) != 2 || len(picture.Targets) != 1 || picture.Targets[0].ID != f.hostA.String() {
		t.Fatalf("picture = %s", d.Picture)
	}
	if !strings.Contains(string(d.Picture), `"big":9007199254740993`) {
		t.Fatalf("picture lost digits of big: %s", d.Picture)
	}
	var requested string
	if err := f.s.DB.Pool().QueryRow(context.Background(),
		`SELECT summary FROM audit_events WHERE action = 'POST requestRun' AND object_id = $1`, out.Request.ID).Scan(&requested); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requested, "dry run") || !strings.Contains(requested, "extra_vars (big, note, version)") ||
		!strings.Contains(requested, "host_ids") || !strings.Contains(requested, "limit") ||
		strings.Contains(requested, "1.2") || strings.Contains(requested, "web-*") {
		t.Fatalf("request audit summary = %q, want the dry run and the override keys, no values", requested)
	}

	resp := f.approve(t, f.adminC, out.Request.ID, d.Fingerprint)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var approved struct {
		Request runRequestDTO `json:"request"`
	}
	decodeInto(t, resp, &approved)
	jobsResp := requestJSON(t, f.adminC, http.MethodGet, f.baseURL()+"?group="+approved.Request.RunGroupID, nil)
	var list struct {
		Jobs []jobDTO `json:"jobs"`
	}
	decodeInto(t, jobsResp, &list)
	if len(list.Jobs) != 1 || list.Jobs[0].ZoneID != f.zoneA.String() || list.Jobs[0].CheckMode == nil || !*list.Jobs[0].CheckMode ||
		list.Jobs[0].DryRun == nil || !list.Jobs[0].DryRun.Diff {
		t.Fatalf("enqueued jobs = %+v", list.Jobs)
	}

	// A worker that honours check mode claims it; the claim guard passes
	// (the picture is unchanged) and the job carries what was approved.
	worker := uuid.Must(uuid.NewV7())
	if _, err := f.s.DB.Pool().Exec(context.Background(),
		`INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ($1, $2, $3, $4, $5)`,
		worker, f.orgID, f.zoneA, "w-overrides", "serial-"+worker.String()); err != nil {
		t.Fatal(err)
	}
	claim, err := f.s.Queue.ClaimFor(context.Background(), worker, f.orgID, []uuid.UUID{f.zoneA}, "v1", jobs.ClaimCaps{CancelStopsPlay: true, SupportsCheckMode: true})
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v %v", claim, err)
	}
	if claim.Job.GetId() != list.Jobs[0].ID || !claim.Job.GetCheck() || !claim.Job.GetDiff() || claim.Job.GetLimit() != "" {
		t.Fatalf("claimed job: id %s check %v diff %v limit %q", claim.Job.GetId(), claim.Job.GetCheck(), claim.Job.GetDiff(), claim.Job.GetLimit())
	}
	// ansiblevars.Inert's form, the webhook path's.
	ev := claim.Job.GetExtraVars()
	if ev["version"] != `{"__ansible_unsafe":"1.2"}` || ev["note"] != `{"__ansible_unsafe":"{{ api_key }}"}` || ev["big"] != "9007199254740993" {
		t.Fatalf("claimed override vars = version %s, note %s, big %s", ev["version"], ev["note"], ev["big"])
	}
	if inv := claim.Job.GetInventoryDoc(); !strings.Contains(inv, "web-1") || strings.Contains(inv, "web-2") {
		t.Fatalf("claimed inventory = %s, want web-1 only", inv)
	}
	if summary := f.approvalSummary(t, out.Request.ID); !strings.Contains(summary, "dry run") || !strings.Contains(summary, "extra_vars (big, note, version)") || strings.Contains(summary, "1.2") {
		t.Fatalf("approval audit summary = %q", summary)
	}
	var status string
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT status FROM jobs WHERE id = $1`, list.Jobs[0].ID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("job status after the claim = %q %v", status, err)
	}
}

// TestSyncFirstDryRunWithOverrides (final review M7): a request that syncs
// first and is a dry run with extra vars on some hosts waits for its sync,
// turns pending with the new commit and its overrides pictured, is
// approved, and its job is claimed in check mode on those hosts only, with
// the vars inert and the synced commit pinned.
func TestSyncFirstDryRunWithOverrides(t *testing.T) {
	f := newRunRequestFixture(t, "rr-ov-sync-first")
	repo := f.repoTemplate(t)
	f.overridesTemplate(t)
	f.recordSync(t, repo, syncedA)
	tok := f.syncToken(t)

	resp := f.s.AssistantDo(t, http.MethodPost, tok, f.orgPath()+"/job-templates/"+f.templateID.String()+"/run-requests", "", map[string]any{
		"reason": "dry run on the fresh head", "sync_first": true, "check_mode": true,
		"extra_vars": map[string]any{"version": "1.2", "note": "{{ api_key }}"}, "host_ids": []string{f.hostA.String()},
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.Code, resp.Body)
	}
	var created syncFirstDetail
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Request.Status != "waiting_for_sync" || created.Request.SyncJobID == "" {
		t.Fatalf("created = %+v", created.Request)
	}

	f.recordSync(t, repo, syncedB)
	f.finishSync(t, created.Request.SyncJobID, jobs.StatusSucceeded, "")
	d := f.syncFirstDetail(t, created.Request.ID)
	var picture struct {
		CheckMode         bool           `json:"check_mode"`
		ExtraVarsOverride map[string]any `json:"extra_vars_override"`
		RequestedHostIDs  []string       `json:"requested_host_ids"`
		Targets           []struct {
			ID string `json:"id"`
		} `json:"targets"`
		Commit *pictureCommit `json:"commit"`
	}
	if err := json.Unmarshal(d.Picture, &picture); err != nil {
		t.Fatal(err)
	}
	if d.Request.Status != "pending" || !picture.CheckMode || picture.ExtraVarsOverride["version"] != "1.2" ||
		len(picture.Targets) != 1 || picture.Targets[0].ID != f.hostA.String() ||
		picture.Commit == nil || picture.Commit.SHA != syncedB || len(picture.Commit.Blocked) != 0 {
		t.Fatalf("after the sync = %+v %s", d.Request, d.Picture)
	}
	if resp := f.approve(t, f.adminC, created.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	worker := uuid.Must(uuid.NewV7())
	if _, err := f.s.DB.Pool().Exec(context.Background(),
		`INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ($1, $2, $3, $4, $5)`,
		worker, f.orgID, f.zoneA, "w-sync-first", "serial-"+worker.String()); err != nil {
		t.Fatal(err)
	}
	claim, err := f.s.Queue.ClaimFor(context.Background(), worker, f.orgID, []uuid.UUID{f.zoneA}, "v1",
		jobs.ClaimCaps{SupportsCheckMode: true, SupportsPinnedCommit: true})
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v %v", claim, err)
	}
	ev := claim.Job.GetExtraVars()
	if !claim.Job.GetCheck() || !claim.Job.GetDiff() || ev["version"] != `{"__ansible_unsafe":"1.2"}` || ev["note"] != `{"__ansible_unsafe":"{{ api_key }}"}` {
		t.Fatalf("claimed: check %v diff %v vars %v", claim.Job.GetCheck(), claim.Job.GetDiff(), ev)
	}
	if inv := claim.Job.GetInventoryDoc(); !strings.Contains(inv, "web-1") || strings.Contains(inv, "web-2") {
		t.Fatalf("claimed inventory = %s, want web-1 only", inv)
	}
	var payload struct {
		Repo struct {
			Commit string `json:"commit"`
		} `json:"repo"`
	}
	if err := json.Unmarshal([]byte(claim.Job.GetKindPayload()), &payload); err != nil || payload.Repo.Commit != syncedB {
		t.Fatalf("claimed repository payload = %s %v, want commit %s", claim.Job.GetKindPayload(), err, syncedB)
	}
}

// TestRunRequestRefusesToReplaceAnInventorySecret (final review I2): a
// template that declares a variable a target host's inventory group keeps
// a secret under still refuses a request setting it; the value is not
// echoed.
func TestRunRequestRefusesToReplaceAnInventorySecret(t *testing.T) {
	f := newRunRequestFixture(t, "rr-ov-inv-secret")
	f.secretGroup(t, "group-s3cret-11", "member-s3cret-22")
	updateTemplate(t, f, func(in *templates.UpdateInput) {
		in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "db_pass", Type: schema.TypeString}, {Name: "version", Type: schema.TypeString}}}
		in.ExtraVars = map[string]any{}
	})
	for _, name := range []string{"db_pass"} {
		resp := f.tryRequest(t, map[string]any{"extra_vars": map[string]any{name: "assistant-chosen-77"}})
		if resp.Code != http.StatusUnprocessableEntity || !strings.Contains(resp.Body.String(), name) || !strings.Contains(resp.Body.String(), "inventory") ||
			strings.Contains(resp.Body.String(), "assistant-chosen-77") {
			t.Fatalf("%s = %d %s, want 422 naming it and the inventory", name, resp.Code, resp.Body)
		}
	}
	if resp := f.tryRequest(t, map[string]any{"extra_vars": map[string]any{"version": "1.2"}}); resp.Code != http.StatusCreated {
		t.Fatalf("a var no secret shares its name with = %d %s", resp.Code, resp.Body)
	}
}

// TestRetryRepeatsTheRunsExtraVars (final review M1): a retry of a run an
// approved request started repeats its extra vars, through a retry request
// (whose picture shows them) and through a person's retry route alike, and
// the retried job carries them inert.
func TestRetryRepeatsTheRunsExtraVars(t *testing.T) {
	f := newRunRequestFixture(t, "rr-ov-retry-vars")
	f.overridesTemplate(t)
	created := f.tryRequest(t, map[string]any{"extra_vars": map[string]any{"version": "1.2"}, "host_ids": []string{f.hostA.String()}})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var out runRequestDetail
	if err := json.Unmarshal(created.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, out.Request.ID)
	if resp := f.approve(t, f.adminC, out.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	worker := uuid.Must(uuid.NewV7())
	if _, err := f.s.DB.Pool().Exec(context.Background(),
		`INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ($1, $2, $3, $4, $5)`,
		worker, f.orgID, f.zoneA, "w-retry", "serial-"+worker.String()); err != nil {
		t.Fatal(err)
	}
	// claimAndFail claims the zone's next job, checks its version var and
	// fails it on web-1.
	claimAndFail := func(what string) string {
		t.Helper()
		claim, err := f.s.Queue.ClaimFor(context.Background(), worker, f.orgID, []uuid.UUID{f.zoneA}, "v1", jobs.ClaimCaps{})
		if err != nil || claim == nil {
			t.Fatalf("%s: claim = %+v %v", what, claim, err)
		}
		if v := claim.Job.GetExtraVars()["version"]; v != `{"__ansible_unsafe":"1.2"}` {
			t.Fatalf("%s: claimed version = %s, want the request's, inert", what, v)
		}
		id := uuid.MustParse(claim.Job.GetId())
		if err := f.s.Queue.FinishFor(context.Background(), jobs.WorkerRef{ID: worker, OrgID: f.orgID, ZoneID: f.zoneA}, id, jobs.StatusFailed,
			map[string]any{jobs.StatsFailedHosts: []any{f.hostA.String()}, jobs.StatsUnreachableHosts: []any{}}, "playbook failed"); err != nil {
			t.Fatalf("%s: finish: %v", what, err)
		}
		return id.String()
	}
	first := claimAndFail("the run")

	retry := f.retryRequest(t, first)
	rd := f.detail(t, retry.Request.ID)
	var picture struct {
		ExtraVarsOverride map[string]any `json:"extra_vars_override"`
	}
	if err := json.Unmarshal(rd.Picture, &picture); err != nil || picture.ExtraVarsOverride["version"] != "1.2" {
		t.Fatalf("retry picture = %s %v", rd.Picture, err)
	}
	if resp := f.approve(t, f.adminC, retry.Request.ID, rd.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve the retry = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	second := claimAndFail("the retry request's run")

	resp := postJSON(t, f.adminC, f.baseURL()+"/"+second+"/retry-failed", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("person's retry = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	claimAndFail("the person's retry")
}

// TestRetryOfVarsNoLongerAllowed (final re-review N3, N4): once the extra
// vars a retry would repeat are no longer allowed (here the template made
// the variable secret), a retry request is refused when it is made, and a
// person's retry is refused naming the original run's variables.
func TestRetryOfVarsNoLongerAllowed(t *testing.T) {
	f := newRunRequestFixture(t, "rr-ov-retry-refused")
	f.overridesTemplate(t)
	created := f.tryRequest(t, map[string]any{"extra_vars": map[string]any{"version": "1.2"}, "host_ids": []string{f.hostA.String()}})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	var out runRequestDetail
	if err := json.Unmarshal(created.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	d := f.detail(t, out.Request.ID)
	if resp := f.approve(t, f.adminC, out.Request.ID, d.Fingerprint); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	list, _, err := f.s.Deps.Jobs.List(f.scoped, f.orgID, jobs.Filter{RunGroupID: &[]uuid.UUID{uuid.MustParse(out.Request.ID)}[0]}, "", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("jobs = %v %v", list, err)
	}
	finishJob(t, f.s, f.zoneA, list[0].ID, jobs.StatusFailed, map[string]any{
		jobs.StatsFailedHosts: []any{f.hostA.String()}, jobs.StatsUnreachableHosts: []any{},
	}, "playbook failed")
	updateTemplate(t, f, func(in *templates.UpdateInput) {
		in.ExtraVarsSchema = &schema.Schema{Fields: []schema.Field{{Name: "version", Type: schema.TypeSecret}, {Name: "api_key", Type: schema.TypeSecret}}}
		in.ExtraVars = map[string]any{"version": "9.9", "api_key": "tmpl-s3cret-77aa"}
	})

	resp := f.s.AssistantDo(t, http.MethodPost, f.tok, f.orgPath()+"/jobs/"+list[0].ID.String()+"/retry-requests", "", map[string]any{"reason": "retry"})
	if resp.Code != http.StatusUnprocessableEntity || !strings.Contains(resp.Body.String(), "version") {
		t.Fatalf("retry request = %d %s, want 422 at create naming version", resp.Code, resp.Body)
	}
	person := postJSON(t, f.adminC, f.baseURL()+"/"+list[0].ID.String()+"/retry-failed", nil)
	body := mustReadBody(t, person)
	if person.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "the extra variables of the run this retries are no longer allowed") ||
		!strings.Contains(body, "version") || strings.Contains(body, "run request") || strings.Contains(body, "1.2") {
		t.Fatalf("person's retry = %d %s", person.StatusCode, body)
	}
}
