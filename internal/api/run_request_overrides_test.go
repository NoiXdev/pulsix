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
		{"extra var of the wrong type", map[string]any{"extra_vars": map[string]any{"version": 3}}, "version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.tryRequest(t, tc.body)
			if resp.Code != http.StatusUnprocessableEntity || !strings.Contains(resp.Body.String(), tc.msg) {
				t.Fatalf("%s = %d %s, want 422 naming %q", tc.name, resp.Code, resp.Body, tc.msg)
			}
		})
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
// claims with the vars, limit and diff the person approved.
func TestRunRequestDryRunWithOverridesRunsAsPictured(t *testing.T) {
	clock := newTestClock()
	f := newRunRequestFixture(t, "rr-ov-run", apitest.WithRunRequestClock(clock.Now))
	f.overridesTemplate(t)
	created := f.tryRequest(t, map[string]any{
		"check_mode": true, "extra_vars": map[string]any{"version": "1.2"}, "host_ids": []string{f.hostA.String()},
		"limit": "web-*", "expires_in": 15,
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
		Hosts             []string       `json:"hosts"`
		Targets           []struct {
			ID string `json:"id"`
		} `json:"targets"`
		FollowUps json.RawMessage `json:"follow_ups"`
	}
	if err := json.Unmarshal(d.Picture, &picture); err != nil {
		t.Fatal(err)
	}
	if !picture.CheckMode || !picture.Diff || picture.LimitOverride != "web-*" || picture.ExtraVarsOverride["version"] != "1.2" ||
		len(picture.Hosts) != 1 || picture.Hosts[0] != f.hostA.String() || len(picture.Targets) != 1 || picture.Targets[0].ID != f.hostA.String() {
		t.Fatalf("picture = %s", d.Picture)
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
	claim, err := f.s.Queue.ClaimFor(context.Background(), worker, f.orgID, []uuid.UUID{f.zoneA}, "v1", true, true)
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v %v", claim, err)
	}
	if claim.Job.GetId() != list.Jobs[0].ID || !claim.Job.GetCheck() || !claim.Job.GetDiff() || claim.Job.GetLimit() != "web-*" {
		t.Fatalf("claimed job: id %s check %v diff %v limit %q", claim.Job.GetId(), claim.Job.GetCheck(), claim.Job.GetDiff(), claim.Job.GetLimit())
	}
	if v := claim.Job.GetExtraVars()["version"]; v != `"1.2"` {
		t.Fatalf("claimed version = %s, want the override", v)
	}
	if inv := claim.Job.GetInventoryDoc(); !strings.Contains(inv, "web-1") || strings.Contains(inv, "web-2") {
		t.Fatalf("claimed inventory = %s, want web-1 only", inv)
	}
	var status string
	if err := f.s.DB.Pool().QueryRow(context.Background(), `SELECT status FROM jobs WHERE id = $1`, list.Jobs[0].ID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("job status after the claim = %q %v", status, err)
	}
}
