// The org monitoring settings routes (backlog 3.57): everyone in the
// organization may read them (org.settings.read), only an admin changes
// them (org.settings.write), the bar the job settings set. The default is
// 14 days before any write; 0..365 round-trips, anything else is a 422.
package api_test

import (
	"net/http"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type orgMonitoringSettingsBody struct {
	Settings struct {
		ClientAbsentDays *int `json:"client_absent_days"`
	} `json:"settings"`
}

func seedMonitoringSettingsOrg(t *testing.T, slug string) (s *apitest.Server, ownerC, operatorC, viewerC *http.Client, url string) {
	t.Helper()
	s = apitest.New(t)
	owner := s.SeedUser(slug+"-owner@example.test", "correct horse battery")
	operator := s.SeedUser(slug+"-operator@example.test", "correct horse battery")
	viewer := s.SeedUser(slug+"-viewer@example.test", "correct horse battery")
	org := s.SeedOrg(slug, owner)
	s.AddMember(org, operator, tenancy.RoleOperator)
	s.AddMember(org, viewer, tenancy.RoleViewer)
	return s, s.LoginAs(owner), s.LoginAs(operator), s.LoginAs(viewer),
		s.HTTP.URL + "/api/v1/orgs/" + slug + "/settings/monitoring"
}

func readAbsentDays(t *testing.T, c *http.Client, url string) int {
	t.Helper()
	resp := requestJSON(t, c, http.MethodGet, url, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}
	var body orgMonitoringSettingsBody
	decodeInto(t, resp, &body)
	if body.Settings.ClientAbsentDays == nil {
		t.Fatal("GET: client_absent_days missing")
	}
	return *body.Settings.ClientAbsentDays
}

func TestOrgMonitoringSettingsDefaultIs14(t *testing.T) {
	_, _, _, viewerC, url := seedMonitoringSettingsOrg(t, "monset-default")
	if got := readAbsentDays(t, viewerC, url); got != 14 {
		t.Fatalf("default client_absent_days = %d, want 14", got)
	}
}

func TestOrgMonitoringSettingsRangeAndRoundTrip(t *testing.T) {
	_, ownerC, _, viewerC, url := seedMonitoringSettingsOrg(t, "monset-range")
	for _, days := range []int{0, 365, 7} {
		resp := requestJSON(t, ownerC, http.MethodPut, url, map[string]any{"client_absent_days": days})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %d: %d %s", days, resp.StatusCode, mustReadBody(t, resp))
		}
		var body orgMonitoringSettingsBody
		decodeInto(t, resp, &body)
		if body.Settings.ClientAbsentDays == nil || *body.Settings.ClientAbsentDays != days {
			t.Fatalf("PUT %d response = %+v", days, body.Settings)
		}
		if got := readAbsentDays(t, viewerC, url); got != days {
			t.Fatalf("GET after PUT %d = %d", days, got)
		}
	}
	for _, body := range []map[string]any{
		{"client_absent_days": 366},
		{"client_absent_days": -1},
		{"client_absent_days": 1.5},
		{},
	} {
		if resp := requestJSON(t, ownerC, http.MethodPut, url, body); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("PUT %v: %d %s, want 422", body, resp.StatusCode, mustReadBody(t, resp))
		}
	}
	if got := readAbsentDays(t, viewerC, url); got != 7 {
		t.Fatalf("after refused writes = %d, want 7", got)
	}
}

func TestOrgMonitoringSettingsPermissionsAndIsolation(t *testing.T) {
	s, ownerC, operatorC, viewerC, url := seedMonitoringSettingsOrg(t, "monset-perm")
	for name, c := range map[string]*http.Client{"viewer": viewerC, "operator": operatorC} {
		if resp := requestJSON(t, c, http.MethodPut, url, map[string]any{"client_absent_days": 3}); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s PUT: %d %s, want 403", name, resp.StatusCode, mustReadBody(t, resp))
		}
	}
	if resp := requestJSON(t, ownerC, http.MethodPut, url, map[string]any{"client_absent_days": 3}); resp.StatusCode != http.StatusOK {
		t.Fatalf("owner PUT: %d %s", resp.StatusCode, mustReadBody(t, resp))
	}

	// Another organization keeps its own default, and its owner cannot
	// reach the first organization's settings.
	other := s.SeedUser("monset-other-owner@example.test", "correct horse battery")
	s.SeedOrg("monset-other", other)
	otherC := s.LoginAs(other)
	if got := readAbsentDays(t, otherC, s.HTTP.URL+"/api/v1/orgs/monset-other/settings/monitoring"); got != 14 {
		t.Fatalf("other org = %d, want its default 14", got)
	}
	if resp := requestJSON(t, otherC, http.MethodGet, url, nil); resp.StatusCode == http.StatusOK {
		t.Fatalf("other org's owner read the first org's settings: %d", resp.StatusCode)
	}
	if resp := requestJSON(t, otherC, http.MethodPut, url, map[string]any{"client_absent_days": 9}); resp.StatusCode == http.StatusOK {
		t.Fatalf("other org's owner wrote the first org's settings: %d", resp.StatusCode)
	}
	if got := readAbsentDays(t, viewerC, url); got != 3 {
		t.Fatalf("first org after the other's attempts = %d, want 3", got)
	}

	// The change is audited.
	activity := decodeActivity(t, requestJSON(t, ownerC, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/monset-perm/activity", nil))
	if len(activity.Items) != 1 || activity.Items[0].Action != "PUT updateOrgMonitoringSettings" || activity.Items[0].ObjectType != "org" {
		t.Fatalf("activity = %+v, want the one settings change", activity.Items)
	}
}
