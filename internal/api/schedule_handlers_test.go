package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/inventory/schema"
	"github.com/noixdev/pulsix/internal/templates"
)

type scheduleOut struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Mode        string         `json:"mode"`
	Cron        string         `json:"cron"`
	Description string         `json:"description"`
	Timezone    string         `json:"timezone"`
	Paused      bool           `json:"paused"`
	NextRunAt   *time.Time     `json:"next_run_at"`
	Revision    int64          `json:"revision"`
	ExtraVars   map[string]any `json:"extra_vars"`
	Guards      struct {
		Overlap string `json:"overlap"`
		CatchUp struct {
			Enabled bool   `json:"enabled"`
			Window  string `json:"window"`
		} `json:"catch_up"`
	} `json:"guards"`
}

func (f runFixture) schedulesURL() string {
	return f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/schedules"
}

func (f runFixture) scheduleBody(name string) map[string]any {
	return map[string]any{
		"template_id": f.templateID.String(), "name": name, "mode": "simple",
		"simple":   map[string]any{"kind": "weekly", "days": []string{"tue", "thu"}, "time": "03:00"},
		"timezone": "Europe/Berlin",
	}
}

func TestScheduleLifecycle(t *testing.T) {
	f := newRunFixture(t, "sched-life")
	c := f.operatorC

	resp := postJSON(t, c, f.schedulesURL(), f.scheduleBody("Nightly OS patching"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	etag := resp.Header.Get("ETag")
	var created struct {
		Schedule scheduleOut `json:"schedule"`
	}
	decodeInto(t, resp, &created)
	s := created.Schedule
	if s.Cron != "0 3 * * 2,4" || s.Description != "Tue, Thu at 03:00" || s.NextRunAt == nil || etag != `"1"` ||
		s.Guards.Overlap != "skip" || s.Guards.CatchUp.Window != "1h" {
		t.Fatalf("created = %+v, etag %q", s, etag)
	}

	// Update needs If-Match: missing is 428, stale is 412.
	up := f.scheduleBody("Nightly OS patching")
	up["mode"], up["cron"] = "cron", "*/30 * * * *"
	delete(up, "simple")
	if resp := requestJSON(t, c, http.MethodPut, f.schedulesURL()+"/"+s.ID, up); resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", resp.StatusCode)
	}
	if resp := requestWithHeaders(t, c, http.MethodPut, f.schedulesURL()+"/"+s.ID, up, map[string]string{"If-Match": `"7"`}); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", resp.StatusCode)
	}
	resp = requestWithHeaders(t, c, http.MethodPut, f.schedulesURL()+"/"+s.ID, up, map[string]string{"If-Match": etag})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("update: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// Pause, resume, run now, runs.
	if resp := postJSON(t, c, f.schedulesURL()+"/"+s.ID+"/pause", nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := postJSON(t, c, f.schedulesURL()+"/"+s.ID+"/resume", nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	resp = postJSON(t, c, f.schedulesURL()+"/"+s.ID+"/run", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal(mustReadBody(t, resp))
	}
	var started struct {
		Jobs []struct {
			Trigger    string `json:"trigger"`
			TriggerRef string `json:"trigger_ref"`
		} `json:"jobs"`
	}
	decodeInto(t, resp, &started)
	if len(started.Jobs) != 1 || started.Jobs[0].Trigger != "schedule" || started.Jobs[0].TriggerRef != s.ID {
		t.Fatalf("run now = %+v", started)
	}
	var runs struct {
		Runs []struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		} `json:"runs"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, f.schedulesURL()+"/"+s.ID+"/runs", nil), &runs)
	if len(runs.Runs) != 1 || runs.Runs[0].Outcome != "triggered" || runs.Runs[0].Reason != "run now" {
		t.Fatalf("runs = %+v", runs)
	}

	// The list filters by template and carries the latest run.
	var list struct {
		Schedules []struct {
			ID      string `json:"id"`
			LastRun *struct {
				Outcome string `json:"outcome"`
			} `json:"last_run"`
		} `json:"schedules"`
	}
	decodeInto(t, requestJSON(t, c, http.MethodGet, f.schedulesURL()+"?template_id="+f.templateID.String(), nil), &list)
	if len(list.Schedules) != 1 || list.Schedules[0].LastRun == nil || list.Schedules[0].LastRun.Outcome != "triggered" {
		t.Fatalf("list = %+v", list)
	}

	var paused struct {
		Paused int `json:"paused"`
	}
	decodeInto(t, postJSON(t, c, f.schedulesURL()+"/pause-all", nil), &paused)
	if paused.Paused != 1 {
		t.Fatalf("pause-all = %+v", paused)
	}
	if resp := requestJSON(t, c, http.MethodDelete, f.schedulesURL()+"/"+s.ID, nil); resp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := requestJSON(t, c, http.MethodGet, f.schedulesURL()+"/"+s.ID, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete: %d", resp.StatusCode)
	}
	for _, action := range []string{"POST createSchedule", "PUT updateSchedule", "POST pauseSchedule", "POST resumeSchedule", "POST runSchedule", "POST pauseAllSchedules", "DELETE deleteSchedule"} {
		assertActivityContains(t, f, action)
	}
}

func TestSchedulePreview(t *testing.T) {
	f := newRunFixture(t, "sched-preview")
	var p struct {
		Cron        string      `json:"cron"`
		Description string      `json:"description"`
		Next        []time.Time `json:"next"`
	}
	resp := postJSON(t, f.viewerC, f.schedulesURL()+"/preview", map[string]any{"mode": "cron", "cron": "0 3 * * *", "timezone": "Europe/Berlin"})
	decodeInto(t, resp, &p)
	if p.Cron != "0 3 * * *" || p.Description != "daily at 03:00" || len(p.Next) != 5 {
		t.Fatalf("preview = %+v", p)
	}
	if resp := postJSON(t, f.viewerC, f.schedulesURL()+"/preview", map[string]any{"mode": "cron", "cron": "61 * * * *", "timezone": "UTC"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad preview: %d", resp.StatusCode)
	}
}

func TestScheduleValidationAndPermissions(t *testing.T) {
	f := newRunFixture(t, "sched-perm")
	// Viewer reads, never writes; nobody outside the org sees anything.
	if resp := requestJSON(t, f.viewerC, http.MethodGet, f.schedulesURL(), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer list: %d", resp.StatusCode)
	}
	if resp := postJSON(t, f.viewerC, f.schedulesURL(), f.scheduleBody("x")); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer create: %d", resp.StatusCode)
	}
	bad := f.scheduleBody("bad")
	bad["timezone"] = "Mars/Olympus"
	if resp := postJSON(t, f.operatorC, f.schedulesURL(), bad); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad timezone: %d", resp.StatusCode)
	}
	// An override the template forbids is a 422 located at the variable.
	bad = f.scheduleBody("bad2")
	bad["extra_vars"] = map[string]any{"ansible_ssh_common_args": "-o X"}
	resp := postJSON(t, f.operatorC, f.schedulesURL(), bad)
	if body := mustReadBody(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "body.extra_vars.ansible_ssh_common_args") {
		t.Fatalf("denylisted override: %d %s", resp.StatusCode, body)
	}
	if resp := postJSON(t, f.operatorC, f.schedulesURL(), f.scheduleBody("dup")); resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := postJSON(t, f.operatorC, f.schedulesURL(), f.scheduleBody("dup")); resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate name: %d", resp.StatusCode)
	}

	// Fix round 1, Minor #1: pause, resume and run now share create's own
	// write gate (scheduleScope with templates.write) but weren't
	// independently HTTP-tested for a viewer.
	created := postJSON(t, f.operatorC, f.schedulesURL(), f.scheduleBody("viewer-write-check"))
	if created.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, created))
	}
	var cs struct {
		Schedule scheduleOut `json:"schedule"`
	}
	decodeInto(t, created, &cs)
	for _, action := range []string{"pause", "resume", "run"} {
		t.Run("viewer "+action, func(t *testing.T) {
			resp := postJSON(t, f.viewerC, f.schedulesURL()+"/"+cs.Schedule.ID+"/"+action, nil)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("viewer %s: %d", action, resp.StatusCode)
			}
		})
	}
}

// TestScheduleExtraVarsSecretsAreNeverLeaked is the brief's "secret values
// never appear in an API response" global constraint, the same class of
// check TestRunDetailLeaksNoSecrets (job_handlers_test.go) makes for the
// run view: a schedule whose extra-var override targets a secret-typed
// field on the template's schema must mask it — schedules.MaskSealed's
// {"$secret":"set"} marker — on every response that carries it (create,
// get, list, update), and neither the sealed ciphertext ($sealed) nor the
// plaintext value may ever reach the wire, or the org's audit trail.
func TestScheduleExtraVarsSecretsAreNeverLeaked(t *testing.T) {
	f := newRunFixture(t, "sched-secret")
	scoped := db.WithOrgScope(context.Background(), f.orgID)
	tmpl, err := f.s.Deps.Templates.Create(scoped, f.orgID, templates.CreateInput{
		Name: "deploy-with-secret", PlaybookID: f.playbook,
		Scope:           templates.Scope{HostIDs: []uuid.UUID{f.hostA}},
		ExtraVarsSchema: &schema.Schema{Fields: []schema.Field{{Name: "db_pass", Type: schema.TypeSecret}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	const secretValue = "s3cret-db-pass-7f2c19"
	c := f.operatorC
	newBody := func(cronTime string) map[string]any {
		return map[string]any{
			"template_id": tmpl.ID.String(), "name": "secret schedule", "mode": "simple",
			"simple":     map[string]any{"kind": "daily", "time": cronTime},
			"timezone":   "Europe/Berlin",
			"extra_vars": map[string]any{"db_pass": secretValue},
		}
	}

	assertNoLeak := func(t *testing.T, label, raw string) {
		t.Helper()
		if !strings.Contains(raw, `"$secret":"set"`) {
			t.Errorf("%s: response does not mask db_pass with the {\"$secret\":\"set\"} marker: %s", label, raw)
		}
		for _, marker := range []string{secretValue, "$sealed"} {
			if strings.Contains(raw, marker) {
				t.Errorf("%s leaks %q: %s", label, marker, raw)
			}
		}
	}

	resp := postJSON(t, c, f.schedulesURL(), newBody("04:00"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	etag := resp.Header.Get("ETag")
	createBody := mustReadBody(t, resp)
	assertNoLeak(t, "create", createBody)
	var created struct {
		Schedule scheduleOut `json:"schedule"`
	}
	if err := json.Unmarshal([]byte(createBody), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Schedule.ID
	if _, sealed := created.Schedule.ExtraVars["db_pass"]; !sealed {
		t.Fatalf("create response has no extra_vars.db_pass at all: %s", createBody)
	}

	getResp := requestJSON(t, c, http.MethodGet, f.schedulesURL()+"/"+id, nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, getResp))
	}
	assertNoLeak(t, "get", mustReadBody(t, getResp))

	listResp := requestJSON(t, c, http.MethodGet, f.schedulesURL()+"?template_id="+tmpl.ID.String(), nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, listResp))
	}
	assertNoLeak(t, "list", mustReadBody(t, listResp))

	updResp := requestWithHeaders(t, c, http.MethodPut, f.schedulesURL()+"/"+id, newBody("05:00"), map[string]string{"If-Match": etag})
	if updResp.StatusCode != http.StatusOK {
		t.Fatal(mustReadBody(t, updResp))
	}
	assertNoLeak(t, "update", mustReadBody(t, updResp))

	// The mutations above (create, update) are audited; the secret must
	// not have leaked into the summary or meta an admin can read back.
	activity := mustReadBody(t, requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/activity?limit=100", nil))
	for _, marker := range []string{secretValue, "$sealed"} {
		if strings.Contains(activity, marker) {
			t.Errorf("activity feed leaks %q: %s", marker, activity)
		}
	}
}

// assertActivityContains fails when the org's activity feed (read by the
// admin) has no event whose action is action.
func assertActivityContains(t *testing.T, f runFixture, action string) {
	t.Helper()
	body := mustReadBody(t, requestJSON(t, f.adminC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/activity?limit=100", nil))
	if !strings.Contains(body, `"`+action+`"`) {
		t.Fatalf("activity lacks %q", action)
	}
}
