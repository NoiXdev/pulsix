// The org monitoring settings routes (backlog 3.57): everyone in the
// organization may read them (org.settings.read), only an admin changes
// them (org.settings.write), the bar the job settings set. The default is
// 14 days before any write; 0..365 round-trips, anything else is a 422.
// Backlog 3.15 adds the retention choice: retention_days (30, 90, 365 or
// null), with the instance maximum and the effective value read-only.
package api_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/api/apitest"
	"github.com/noixdev/pulsix/internal/tenancy"
)

type orgMonitoringSettingsBody struct {
	Settings struct {
		ClientAbsentDays       *int            `json:"client_absent_days"`
		RetentionDays          json.RawMessage `json:"retention_days"`
		RetentionMaxDays       *int            `json:"retention_max_days"`
		RetentionEffectiveDays *int            `json:"retention_effective_days"`
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

// retentionView is the retention half of the settings as the API returns
// it; choice is "null" when no choice is stored.
type retentionView struct {
	choice         string
	max, effective int
}

func readRetention(t *testing.T, c *http.Client, method, url string, body any) retentionView {
	t.Helper()
	resp := requestJSON(t, c, method, url, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %v: %d %s", method, body, resp.StatusCode, mustReadBody(t, resp))
	}
	var b orgMonitoringSettingsBody
	decodeInto(t, resp, &b)
	if b.Settings.RetentionDays == nil || b.Settings.RetentionMaxDays == nil || b.Settings.RetentionEffectiveDays == nil {
		t.Fatalf("%s: retention fields missing: %+v", method, b.Settings)
	}
	return retentionView{string(b.Settings.RetentionDays), *b.Settings.RetentionMaxDays, *b.Settings.RetentionEffectiveDays}
}

// TestOrgMonitoringSettingsRetention: without a choice the instance
// maximum (90 in the test server) applies; a choice round-trips with its
// effective value; omitting retention_days keeps it (the SPA's absent-days
// form sends only client_absent_days); null clears it; a choice above the
// maximum is stored but capped; anything but 30, 90, 365 or null is a 422.
func TestOrgMonitoringSettingsRetention(t *testing.T) {
	_, ownerC, _, viewerC, url := seedMonitoringSettingsOrg(t, "monset-retention")

	if got := readRetention(t, viewerC, http.MethodGet, url, nil); got != (retentionView{"null", 90, 90}) {
		t.Fatalf("default = %+v, want no choice, max 90, effective 90", got)
	}
	if got := readRetention(t, ownerC, http.MethodPut, url, map[string]any{"client_absent_days": 14, "retention_days": 30}); got != (retentionView{"30", 90, 30}) {
		t.Fatalf("PUT 30 = %+v, want 30/90/30", got)
	}
	if got := readRetention(t, ownerC, http.MethodPut, url, map[string]any{"client_absent_days": 3}); got != (retentionView{"30", 90, 30}) {
		t.Fatalf("PUT without retention_days = %+v, want the kept 30", got)
	}
	if got := readAbsentDays(t, viewerC, url); got != 3 {
		t.Fatalf("client_absent_days after the PUT without retention = %d, want 3", got)
	}
	if got := readRetention(t, ownerC, http.MethodPut, url, map[string]any{"client_absent_days": 3, "retention_days": 365}); got != (retentionView{"365", 90, 90}) {
		t.Fatalf("PUT 365 under max 90 = %+v, want 365/90/90", got)
	}
	if got := readRetention(t, ownerC, http.MethodPut, url, map[string]any{"client_absent_days": 3, "retention_days": nil}); got != (retentionView{"null", 90, 90}) {
		t.Fatalf("PUT null = %+v, want no choice", got)
	}
	if got := readRetention(t, viewerC, http.MethodGet, url, nil); got != (retentionView{"null", 90, 90}) {
		t.Fatalf("GET after clearing = %+v, want no choice", got)
	}

	if got := readRetention(t, ownerC, http.MethodPut, url, map[string]any{"client_absent_days": 3, "retention_days": 90}); got.choice != "90" {
		t.Fatalf("PUT 90 = %+v", got)
	}
	for _, v := range []any{0, 7, 31, 60, 366, -30, 30.5, "30"} {
		if resp := requestJSON(t, ownerC, http.MethodPut, url, map[string]any{"client_absent_days": 3, "retention_days": v}); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("PUT retention_days %v: %d %s, want 422", v, resp.StatusCode, mustReadBody(t, resp))
		}
	}
	// client_absent_days stays required.
	if resp := requestJSON(t, ownerC, http.MethodPut, url, map[string]any{"retention_days": 30}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("PUT without client_absent_days: %d %s, want 422", resp.StatusCode, mustReadBody(t, resp))
	}
	if got := readRetention(t, viewerC, http.MethodGet, url, nil); got.choice != "90" {
		t.Fatalf("after refused writes = %+v, want 90", got)
	}

	// A GET's settings object can be sent back as is: the read-only fields
	// are accepted and ignored.
	roundTrip := map[string]any{
		"client_absent_days": 3, "retention_days": 30,
		"retention_max_days": 7, "retention_effective_days": 7,
	}
	if got := readRetention(t, ownerC, http.MethodPut, url, roundTrip); got != (retentionView{"30", 90, 30}) {
		t.Fatalf("PUT with the read-only fields = %+v, want 30/90/30 (read-only fields ignored)", got)
	}
}

// TestOrgMonitoringSettingsRetentionUnderALowerMaximum: with
// PULSIX_SAMPLE_RETENTION at 30 every choice is capped at 30.
func TestOrgMonitoringSettingsRetentionUnderALowerMaximum(t *testing.T) {
	s := apitest.New(t, apitest.WithRetentionMaxDays(30))
	owner := s.SeedUser("monset-lowmax-owner@example.test", "correct horse battery")
	s.SeedOrg("monset-lowmax", owner)
	c := s.LoginAs(owner)
	url := s.HTTP.URL + "/api/v1/orgs/monset-lowmax/settings/monitoring"
	if got := readRetention(t, c, http.MethodGet, url, nil); got != (retentionView{"null", 30, 30}) {
		t.Fatalf("default under max 30 = %+v", got)
	}
	if got := readRetention(t, c, http.MethodPut, url, map[string]any{"client_absent_days": 14, "retention_days": 90}); got != (retentionView{"90", 30, 30}) {
		t.Fatalf("PUT 90 under max 30 = %+v, want 90/30/30", got)
	}
}

// TestOrgMonitoringSettingsRetentionPermissionsAndIsolation: viewers and
// operators read the retention but cannot change it; another
// organization's choice is its own.
func TestOrgMonitoringSettingsRetentionPermissionsAndIsolation(t *testing.T) {
	s, ownerC, operatorC, viewerC, url := seedMonitoringSettingsOrg(t, "monset-ret-perm")
	for name, c := range map[string]*http.Client{"viewer": viewerC, "operator": operatorC} {
		if resp := requestJSON(t, c, http.MethodPut, url, map[string]any{"client_absent_days": 14, "retention_days": 30}); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s PUT: %d %s, want 403", name, resp.StatusCode, mustReadBody(t, resp))
		}
		if got := readRetention(t, c, http.MethodGet, url, nil); got.choice != "null" {
			t.Fatalf("%s GET = %+v, want no choice", name, got)
		}
	}
	readRetention(t, ownerC, http.MethodPut, url, map[string]any{"client_absent_days": 14, "retention_days": 30})

	other := s.SeedUser("monset-ret-other@example.test", "correct horse battery")
	s.SeedOrg("monset-ret-other", other)
	otherC := s.LoginAs(other)
	if got := readRetention(t, otherC, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/monset-ret-other/settings/monitoring", nil); got.choice != "null" {
		t.Fatalf("other org = %+v, want its own default", got)
	}
	if resp := requestJSON(t, otherC, http.MethodPut, url, map[string]any{"client_absent_days": 14, "retention_days": 365}); resp.StatusCode == http.StatusOK {
		t.Fatalf("other org's owner wrote the first org's retention: %d", resp.StatusCode)
	}
	if got := readRetention(t, viewerC, http.MethodGet, url, nil); got.choice != "30" {
		t.Fatalf("first org after the other's attempt = %+v, want 30", got)
	}
}

// TestOrgMonitoringSettingsAuditNamesTheRetentionChange: a write that
// changes the retention says so in the audit summary (from → to, "the
// instance default" for no choice); a write without retention_days, or
// with the stored value, keeps the plain summary.
func TestOrgMonitoringSettingsAuditNamesTheRetentionChange(t *testing.T) {
	s, ownerC, _, _, url := seedMonitoringSettingsOrg(t, "monset-ret-audit")
	puts := []map[string]any{
		{"client_absent_days": 14, "retention_days": 30},
		{"client_absent_days": 9},
		{"client_absent_days": 9, "retention_days": 30},
		{"client_absent_days": 9, "retention_days": nil},
	}
	for _, body := range puts {
		readRetention(t, ownerC, http.MethodPut, url, body)
	}
	activity := decodeActivity(t, requestJSON(t, ownerC, http.MethodGet, s.HTTP.URL+"/api/v1/orgs/monset-ret-audit/activity", nil))
	var summaries []string
	for _, e := range activity.Items {
		summaries = append(summaries, e.Summary)
	}
	slices.Reverse(summaries) // the activity lists the newest first
	want := []string{
		"updated the organization's monitoring settings; measurement retention from the instance default to 30 days",
		"updated the organization's monitoring settings",
		"updated the organization's monitoring settings",
		"updated the organization's monitoring settings; measurement retention from 30 days to the instance default",
	}
	if !slices.Equal(summaries, want) {
		t.Fatalf("audit summaries = %q, want %q", summaries, want)
	}
}
