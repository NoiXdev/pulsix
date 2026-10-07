package api_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestCalendar(t *testing.T) {
	f := newRunFixture(t, "calendar")
	body := f.scheduleBody("Nightly")
	body["mode"], body["cron"], body["timezone"] = "cron", "0 3 * * *", "UTC"
	delete(body, "simple")
	if resp := postJSON(t, f.operatorC, f.schedulesURL(), body); resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	now := time.Now().UTC()
	win := map[string]any{"name": "db upgrade", "target_kind": "org",
		"starts_at": now.Add(24 * time.Hour).Format(time.RFC3339), "ends_at": now.Add(26 * time.Hour).Format(time.RFC3339)}
	if resp := postJSON(t, f.operatorC, f.maintenanceURL(), win); resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	q := url.Values{"from": {now.Format(time.RFC3339)}, "to": {now.Add(7 * 24 * time.Hour).Format(time.RFC3339)}}
	var cal struct {
		Entries []struct {
			ScheduleName string `json:"schedule_name"`
			State        string `json:"state"`
		} `json:"entries"`
		Maintenance []struct {
			Name   string `json:"name"`
			Reason string `json:"reason"`
		} `json:"maintenance"`
	}
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/calendar?"+q.Encode(), nil), &cal)
	if len(cal.Entries) != 7 || cal.Entries[0].ScheduleName != "Nightly" || cal.Entries[0].State != "planned" {
		t.Fatalf("entries = %+v, want 7 planned nightly runs", cal.Entries)
	}
	// Reason threads through from internal/maintenance's own Occurrence
	// (v0.4a Task 8): the window above was created with no reason, so the
	// calendar must report the service's own "maintenance" default, not
	// an empty string the DTO happened to leave unset.
	if len(cal.Maintenance) != 1 || cal.Maintenance[0].Name != "db upgrade" || cal.Maintenance[0].Reason != "maintenance" {
		t.Fatalf("maintenance = %+v, want one db-upgrade window with reason=maintenance", cal.Maintenance)
	}
	for _, bad := range []url.Values{
		{"from": {"yesterday"}, "to": {now.Format(time.RFC3339)}},
		{"from": {now.Format(time.RFC3339)}, "to": {now.Add(43 * 24 * time.Hour).Format(time.RFC3339)}},
	} {
		if resp := requestJSON(t, f.viewerC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/calendar?"+bad.Encode(), nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400", bad, resp.StatusCode)
		}
	}
}

// TestCalendarReportsTruncatedMaintenance is the final review's Important
// #3. maintenance.Occurrences stops at 500 per window and said nothing
// about it, and calendar.Range set Truncated only when the *total* crossed
// its own 5000 cap — so a single frequent window returned its first 500
// stretches with `truncated: false`, and the SPA faithfully drew the rest
// of the range as free of maintenance. An operator then plans a deploy
// into a window the API told them was not there. The planned-occurrence
// path already reports its own per-schedule cap correctly; this is the
// sibling that was missing.
//
// A */15 window over seven days is 672 occurrences, well past the cap.
func TestCalendarReportsTruncatedMaintenance(t *testing.T) {
	f := newRunFixture(t, "calendar-trunc")
	win := map[string]any{"name": "agent restart", "target_kind": "org", "cron": "*/15 * * * *", "duration": "5m", "timezone": "UTC"}
	if resp := postJSON(t, f.operatorC, f.maintenanceURL(), win); resp.StatusCode != http.StatusCreated {
		t.Fatal(mustReadBody(t, resp))
	}
	now := time.Now().UTC()
	q := url.Values{"from": {now.Format(time.RFC3339)}, "to": {now.Add(7 * 24 * time.Hour).Format(time.RFC3339)}}
	var cal struct {
		Truncated   bool `json:"truncated"`
		Maintenance []struct {
			Name string `json:"name"`
		} `json:"maintenance"`
	}
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/calendar?"+q.Encode(), nil), &cal)
	if len(cal.Maintenance) != 500 {
		t.Fatalf("maintenance = %d, want the per-window cap of 500", len(cal.Maintenance))
	}
	if !cal.Truncated {
		t.Fatal("truncated = false although the window's occurrences were capped; the API withheld data while saying it had not")
	}
}
