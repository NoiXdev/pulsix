package api_test

import (
	"net/http"
	"testing"
)

// TestScheduleMaintenanceGuardRoundTrips: the guard defaults to ignore,
// takes skip and exclude_hosts, and refuses anything else.
func TestScheduleMaintenanceGuardRoundTrips(t *testing.T) {
	f := newRunFixture(t, "sched-maint")
	body := f.scheduleBody("Nightly")
	resp := postJSON(t, f.operatorC, f.schedulesURL(), body)
	var created struct {
		Schedule struct {
			ID     string `json:"id"`
			Guards struct {
				Maintenance string `json:"maintenance"`
			} `json:"guards"`
		} `json:"schedule"`
	}
	decodeInto(t, resp, &created)
	if created.Schedule.Guards.Maintenance != "ignore" {
		t.Fatalf("default maintenance guard = %q", created.Schedule.Guards.Maintenance)
	}
	body["name"], body["guards"] = "Guarded", map[string]any{"maintenance": "exclude_hosts"}
	resp = postJSON(t, f.operatorC, f.schedulesURL(), body)
	decodeInto(t, resp, &created)
	if created.Schedule.Guards.Maintenance != "exclude_hosts" {
		t.Fatalf("maintenance guard = %q", created.Schedule.Guards.Maintenance)
	}
	body["name"], body["guards"] = "Bad", map[string]any{"maintenance": "sometimes"}
	if resp := postJSON(t, f.operatorC, f.schedulesURL(), body); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown guard = %d", resp.StatusCode)
	}
}
