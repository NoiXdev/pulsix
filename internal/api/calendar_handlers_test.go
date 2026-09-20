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
			Name string `json:"name"`
		} `json:"maintenance"`
	}
	decodeInto(t, requestJSON(t, f.viewerC, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/calendar?"+q.Encode(), nil), &cal)
	if len(cal.Entries) != 7 || cal.Entries[0].ScheduleName != "Nightly" || cal.Entries[0].State != "planned" {
		t.Fatalf("entries = %+v, want 7 planned nightly runs", cal.Entries)
	}
	if len(cal.Maintenance) != 1 || cal.Maintenance[0].Name != "db upgrade" {
		t.Fatalf("maintenance = %+v", cal.Maintenance)
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
