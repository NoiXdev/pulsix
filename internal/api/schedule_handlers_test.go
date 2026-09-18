package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
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
