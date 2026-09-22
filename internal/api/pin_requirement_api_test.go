// The pin requirement's end-to-end behavior through the HTTP routes
// (backlog 2.44): with jobs_require_pinned_hosts on, a run against a
// template whose scope names an unpinned host is refused with 422 naming
// the host; with the setting off, the same run enqueues. The unit tests in
// the jobs package cover the other enqueue paths and the pass-when-off
// matrix; this one proves the whole HTTP stack — route, permission, error
// mapping — end to end.
package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/jobs"
)

// TestRunRouteRefusesUnpinnedHostsWhenTheOrgRequiresPins drives the run
// action through the API with the requirement on and off.
func TestRunRouteRefusesUnpinnedHostsWhenTheOrgRequiresPins(t *testing.T) {
	f := newRunFixture(t, "pinreq-run")
	settingsURL := f.s.HTTP.URL + "/api/v1/orgs/" + f.orgSlug + "/settings/jobs"

	if resp := requestJSON(t, f.adminC, http.MethodPut, settingsURL, map[string]any{"jobs_require_pinned_hosts": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT settings: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	resp := postJSON(t, f.adminC, f.runURL(), nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("run with the requirement on: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if body := mustReadBody(t, resp); !strings.Contains(body, f.hostA.String()) {
		t.Fatalf("refusal %q must name the unpinned host %s", body, f.hostA)
	}
	// No job was created: the refusal happens before any insert.
	listed, _, err := f.s.Deps.Jobs.List(context.Background(), f.orgID, jobs.Filter{}, "", 10)
	if err != nil || len(listed) != 0 {
		t.Fatalf("jobs after refusal = %d, %v; want none", len(listed), err)
	}

	if resp := requestJSON(t, f.adminC, http.MethodPut, settingsURL, map[string]any{"jobs_require_pinned_hosts": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT settings off: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := postJSON(t, f.adminC, f.runURL(), nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run with the requirement off: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}
