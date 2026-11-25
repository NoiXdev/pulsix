package tenancy_test

import (
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/tenancy"
)

func TestAreaPermissions(t *testing.T) {
	cases := map[tenancy.Area][]tenancy.Permission{
		tenancy.AreaInventoryRead:     {tenancy.PermHostsRead, tenancy.PermInventoryRead},
		tenancy.AreaInventoryWrite:    {tenancy.PermHostsRead, tenancy.PermInventoryRead, tenancy.PermHostsWrite, tenancy.PermInventoryWrite},
		tenancy.AreaReposRead:         {tenancy.PermReposRead, tenancy.PermPlaybooksRead},
		tenancy.AreaReposWrite:        {tenancy.PermReposRead, tenancy.PermPlaybooksRead, tenancy.PermReposWrite, tenancy.PermPlaybooksWrite},
		tenancy.AreaTemplatesRead:     {tenancy.PermTemplatesRead},
		tenancy.AreaTemplatesWrite:    {tenancy.PermTemplatesRead, tenancy.PermTemplatesWrite},
		tenancy.AreaCredentialsNames:  {tenancy.PermCredentialsRead},
		tenancy.AreaJobsRead:          {tenancy.PermJobsRead},
		tenancy.AreaJobsRequest:       {tenancy.PermJobsRead, tenancy.PermJobsRequest},
		tenancy.AreaMonitoringRead:    {tenancy.PermAgentsRead, tenancy.PermChecksRead, tenancy.PermMaintenanceRead},
		tenancy.AreaAlertsHandle:      {tenancy.PermAgentsRead, tenancy.PermChecksRead, tenancy.PermMaintenanceRead, tenancy.PermAlertsHandle},
		tenancy.AreaActivityRead:      {tenancy.PermAuditRead},
		tenancy.AreaAutomationRequest: {tenancy.PermJobsRead, tenancy.PermAutomationRequest},
	}
	for area, want := range cases {
		got, ok := tenancy.AreaPermissions(area)
		if !ok {
			t.Fatalf("%s: not a known area", area)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", area, got, want)
		}
	}
	if len(tenancy.Areas()) != len(cases) {
		t.Errorf("Areas() lists %d areas, the table %d", len(tenancy.Areas()), len(cases))
	}
}

func TestNoAreaGrantsJobsRun(t *testing.T) {
	for _, a := range tenancy.Areas() {
		if tenancy.AreasAllow([]tenancy.Area{a}, tenancy.PermJobsRun) {
			t.Errorf("area %s grants jobs.run", a)
		}
	}
}

func TestAreasAllow(t *testing.T) {
	areas := []tenancy.Area{tenancy.AreaInventoryRead, tenancy.AreaJobsRead}
	if !tenancy.AreasAllow(areas, tenancy.PermHostsRead) {
		t.Error("inventory.read should allow hosts.read")
	}
	if tenancy.AreasAllow(areas, tenancy.PermHostsWrite) {
		t.Error("inventory.read must not allow hosts.write")
	}
	if tenancy.AreasAllow(nil, tenancy.PermHostsRead) {
		t.Error("no areas must allow nothing")
	}
}

func TestAreasAllowUnknownAreaFailsClosed(t *testing.T) {
	if tenancy.AreasAllow([]tenancy.Area{"everything"}, tenancy.PermHostsRead) {
		t.Error("an unknown area granted a permission")
	}
	if tenancy.ValidArea("everything") {
		t.Error("an unknown area is valid")
	}
	if !tenancy.ValidArea(tenancy.AreaJobsRequest) {
		t.Error("jobs.request is not valid")
	}
}

func TestJobsRequestIsOperator(t *testing.T) {
	if tenancy.Allows(tenancy.RoleViewer, tenancy.PermJobsRequest) {
		t.Error("a viewer may request runs")
	}
	if !tenancy.Allows(tenancy.RoleOperator, tenancy.PermJobsRequest) {
		t.Error("an operator may not request runs")
	}
}

func TestRoleAllowsArea(t *testing.T) {
	if tenancy.RoleAllowsArea(tenancy.RoleViewer, tenancy.AreaInventoryWrite) {
		t.Error("a viewer role covers inventory.write")
	}
	if !tenancy.RoleAllowsArea(tenancy.RoleViewer, tenancy.AreaInventoryRead) {
		t.Error("a viewer role does not cover inventory.read")
	}
	if tenancy.RoleAllowsArea(tenancy.RoleViewer, tenancy.AreaCredentialsNames) {
		t.Error("a viewer role covers credentials.names (credentials.read is operator)")
	}
}

func TestNewAreas(t *testing.T) {
	if !tenancy.RoleAllowsArea(tenancy.RoleViewer, tenancy.AreaMonitoringRead) {
		t.Error("a viewer role does not cover monitoring.read")
	}
	if tenancy.RoleAllowsArea(tenancy.RoleViewer, tenancy.AreaAlertsHandle) {
		t.Error("a viewer role covers alerts.handle")
	}
	if !tenancy.RoleAllowsArea(tenancy.RoleOperator, tenancy.AreaAlertsHandle) {
		t.Error("an operator role does not cover alerts.handle")
	}
	if tenancy.RoleAllowsArea(tenancy.RoleViewer, tenancy.AreaActivityRead) {
		t.Error("a viewer role covers activity.read")
	}
	if !tenancy.RoleAllowsArea(tenancy.RoleOperator, tenancy.AreaActivityRead) {
		t.Error("an operator role does not cover activity.read")
	}
	if !tenancy.AreasAllow([]tenancy.Area{tenancy.AreaAlertsHandle}, tenancy.PermChecksRead) {
		t.Error("alerts.handle does not include checks.read")
	}
	if tenancy.AreasAllow([]tenancy.Area{tenancy.AreaAlertsHandle}, tenancy.PermChecksWrite) {
		t.Error("alerts.handle grants checks.write")
	}
	all := tenancy.Areas()
	want := []tenancy.Area{tenancy.AreaMonitoringRead, tenancy.AreaAlertsHandle, tenancy.AreaActivityRead}
	if len(all) < len(want) {
		t.Fatalf("Areas() = %v", all)
	}
	if got := all[len(all)-4 : len(all)-1]; !slices.Equal(got, want) {
		t.Errorf("Areas() ends with %v, want %v", got, want)
	}
}

func TestAutomationRequestArea(t *testing.T) {
	if tenancy.RoleAllowsArea(tenancy.RoleViewer, tenancy.AreaAutomationRequest) {
		t.Error("a viewer role covers automation.request")
	}
	if !tenancy.RoleAllowsArea(tenancy.RoleOperator, tenancy.AreaAutomationRequest) {
		t.Error("an operator role does not cover automation.request")
	}
	for _, p := range []tenancy.Permission{tenancy.PermTemplatesWrite, tenancy.PermReposWrite, tenancy.PermJobsRun, tenancy.PermAgentsWrite} {
		if tenancy.AreasAllow([]tenancy.Area{tenancy.AreaAutomationRequest}, p) {
			t.Errorf("automation.request grants %s", p)
		}
	}
	if all := tenancy.Areas(); all[len(all)-1] != tenancy.AreaAutomationRequest {
		t.Errorf("Areas() = %v, want automation.request last", all)
	}
}
