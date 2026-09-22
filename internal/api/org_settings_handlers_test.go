// The org job settings routes (backlog 2.44): everyone in the
// organization may read the toggle (org.settings.read), only an admin
// changes it (org.settings.write) — the same shape the channels routes
// give their own setting. The default read is false before any write, the
// write round-trips, and it is audited.
package api_test

import (
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type orgJobSettingsBody struct {
	Settings struct {
		JobsRequirePinnedHosts bool `json:"jobs_require_pinned_hosts"`
	} `json:"settings"`
}

func seedOrgSettingsOrg(t *testing.T) (s *apitest.Server, ownerC, operatorC, viewerC *http.Client, url string) {
	t.Helper()
	s = apitest.New(t)
	owner := s.SeedUser("orgsettings-owner@example.test", "correct horse battery")
	operator := s.SeedUser("orgsettings-operator@example.test", "correct horse battery")
	viewer := s.SeedUser("orgsettings-viewer@example.test", "correct horse battery")
	org := s.SeedOrg("orgsettings-org", owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	return s, s.LoginAs(owner), s.LoginAs(operator), s.LoginAs(viewer),
		s.HTTP.URL + "/api/v1/orgs/orgsettings-org/settings/jobs"
}

func TestOrgJobSettingsDefaultIsOff(t *testing.T) {
	_, _, _, viewerC, url := seedOrgSettingsOrg(t)
	var body orgJobSettingsBody
	decodeInto(t, requestJSON(t, viewerC, http.MethodGet, url, nil), &body)
	if body.Settings.JobsRequirePinnedHosts {
		t.Fatalf("default settings = %+v, want jobs_require_pinned_hosts false", body.Settings)
	}
}

func TestOrgJobSettingsRoundTrip(t *testing.T) {
	_, ownerC, _, _, url := seedOrgSettingsOrg(t)
	resp := requestJSON(t, ownerC, http.MethodPut, url, map[string]any{"jobs_require_pinned_hosts": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var body orgJobSettingsBody
	decodeInto(t, resp, &body)
	if !body.Settings.JobsRequirePinnedHosts {
		t.Fatalf("PUT response = %+v, want true", body.Settings)
	}
	var after orgJobSettingsBody
	decodeInto(t, requestJSON(t, ownerC, http.MethodGet, url, nil), &after)
	if !after.Settings.JobsRequirePinnedHosts {
		t.Fatalf("GET after PUT = %+v, want true", after.Settings)
	}
}

func TestOrgJobSettingsPermissionMatrix(t *testing.T) {
	s, ownerC, operatorC, viewerC, url := seedOrgSettingsOrg(t)

	// Reads are org.settings.read (viewer).
	if resp := requestJSON(t, viewerC, http.MethodGet, url, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer GET: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// Writes are org.settings.write: viewer and operator are forbidden.
	if resp := requestJSON(t, viewerC, http.MethodPut, url, map[string]any{"jobs_require_pinned_hosts": true}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer PUT: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, operatorC, http.MethodPut, url, map[string]any{"jobs_require_pinned_hosts": true}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator PUT: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	if resp := requestJSON(t, ownerC, http.MethodPut, url, map[string]any{"jobs_require_pinned_hosts": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("owner PUT: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	// Cross-org: the settings of another organization are not reachable
	// even by its own default route shape.
	other := s.SeedUser("orgsettings-other@example.test", "correct horse battery")
	s.SeedOrg("orgsettings-other", other)
	if resp := requestJSON(t, s.LoginAs(other), http.MethodGet,
		s.HTTP.URL+"/api/v1/orgs/orgsettings-other/settings/jobs", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("other org GET: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
}

func TestOrgJobSettingsAuditRecordsTheChange(t *testing.T) {
	s, ownerC, _, _, url := seedOrgSettingsOrg(t)
	if resp := requestJSON(t, ownerC, http.MethodPut, url, map[string]any{"jobs_require_pinned_hosts": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	activity := decodeActivity(t, requestJSON(t, ownerC, http.MethodGet,
		s.HTTP.URL+"/api/v1/orgs/orgsettings-org/activity", nil))
	if len(activity.Items) != 1 {
		t.Fatalf("activity = %+v, want exactly the settings change", activity)
	}
	if ev := activity.Items[0]; ev.Action != "PUT updateOrgJobSettings" || ev.ObjectType != "org" {
		t.Fatalf("event = (%q, %q), want (PUT updateOrgJobSettings, org)", ev.Action, ev.ObjectType)
	}
}
