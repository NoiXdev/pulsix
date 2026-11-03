package api_test

import (
	"net/http"
	"testing"
)

// TestRunsListFiltersByTrigger: the runs list narrows to one trigger kind
// and to the runs one schedule started (spec 2026-09-18 §8).
func TestRunsListFiltersByTrigger(t *testing.T) {
	f := newRunFixture(t, "runs-trigger")
	c := f.operatorC
	resp := postJSON(t, c, f.schedulesURL(), f.scheduleBody("Nightly"))
	var created struct {
		Schedule struct {
			ID string `json:"id"`
		} `json:"schedule"`
	}
	decodeInto(t, resp, &created)
	if resp := postJSON(t, c, f.schedulesURL()+"/"+created.Schedule.ID+"/run", nil); resp.StatusCode != http.StatusAccepted {
		t.Fatal(mustReadBody(t, resp))
	}
	if resp := postJSON(t, c, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/job-templates/"+f.templateID.String()+"/run", nil); resp.StatusCode != http.StatusAccepted {
		t.Fatal(mustReadBody(t, resp))
	}
	list := func(query string) []string {
		t.Helper()
		var out struct {
			Jobs []struct {
				Trigger string `json:"trigger"`
			} `json:"jobs"`
		}
		decodeInto(t, requestJSON(t, c, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/jobs?"+query, nil), &out)
		triggers := make([]string, len(out.Jobs))
		for i, j := range out.Jobs {
			triggers[i] = j.Trigger
		}
		return triggers
	}
	if got := list("trigger=schedule"); len(got) != 1 || got[0] != "schedule" {
		t.Fatalf("trigger=schedule = %v", got)
	}
	if got := list("trigger=manual"); len(got) != 1 || got[0] != "manual" {
		t.Fatalf("trigger=manual = %v", got)
	}
	if got := list("trigger_ref=" + created.Schedule.ID); len(got) != 1 {
		t.Fatalf("trigger_ref = %v", got)
	}
	if got := list("trigger=webhook"); len(got) != 0 {
		t.Fatalf("trigger=webhook = %v", got)
	}
	for _, bad := range []string{"trigger=cron", "trigger_ref=not-a-uuid"} {
		if resp := requestJSON(t, c, http.MethodGet, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/jobs?"+bad, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, resp.StatusCode)
		}
	}
}

// TestRunsListFiltersByHost: host_id narrows the list to the runs that
// targeted the host (jobs.host_ids), and a malformed id is a 400.
func TestRunsListFiltersByHost(t *testing.T) {
	f := newRunFixture(t, "runs-host")
	c := f.operatorC
	if resp := postJSON(t, c, f.s.HTTP.URL+"/api/v1/orgs/"+f.orgSlug+"/job-templates/"+f.templateID.String()+"/run", nil); resp.StatusCode != http.StatusAccepted {
		t.Fatal(mustReadBody(t, resp))
	}
	base := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/jobs"
	count := func(query string) int {
		t.Helper()
		var out struct {
			Jobs []struct{} `json:"jobs"`
		}
		decodeInto(t, requestJSON(t, c, http.MethodGet, base+"?"+query, nil), &out)
		return len(out.Jobs)
	}
	if n := count("host_id=" + f.hostA.String()); n != 1 {
		t.Fatalf("host_id=<targeted host> = %d jobs, want 1", n)
	}
	if n := count("host_id=" + f.hostB.String()); n != 0 {
		t.Fatalf("host_id=<host the template does not scope> = %d jobs, want 0", n)
	}
	if resp := requestJSON(t, c, http.MethodGet, base+"?host_id=not-a-uuid", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("host_id=not-a-uuid = %d, want 400", resp.StatusCode)
	}
}
