package tenancy

import "slices"

// Area is a named set of permissions an assistant token may be granted
// (spec docs/superpowers/specs/2026-11-16-assistant-access-design.md,
// "Assistant tokens"). A token stores the areas its owner chose rather than
// the permissions they expand to, so the mapping lives here, in one place,
// and a token never silently gains a permission that was added to the
// matrix later.
type Area string

// The areas. A write area includes its read area.
const (
	AreaInventoryRead    Area = "inventory.read"
	AreaInventoryWrite   Area = "inventory.write"
	AreaReposRead        Area = "repos.read"
	AreaReposWrite       Area = "repos.write"
	AreaTemplatesRead    Area = "templates.read"
	AreaTemplatesWrite   Area = "templates.write"
	AreaCredentialsNames Area = "credentials.names"
	AreaJobsRead         Area = "jobs.read"
	AreaJobsRequest      Area = "jobs.request"
	AreaMonitoringRead   Area = "monitoring.read"
	AreaAlertsHandle     Area = "alerts.handle"
	AreaActivityRead     Area = "activity.read"
	// AreaAutomationRequest asks for schedules (and, later, webhook
	// triggers) that a person then approves (spec 2026-11-25 assistant API
	// gaps, C), and pauses or deletes them directly: stopping runs needs
	// no approval (decision D1).
	AreaAutomationRequest Area = "automation.request"
)

// areaOrder is the canonical order Areas returns.
var areaOrder = []Area{
	AreaInventoryRead, AreaInventoryWrite,
	AreaReposRead, AreaReposWrite,
	AreaTemplatesRead, AreaTemplatesWrite,
	AreaCredentialsNames,
	AreaJobsRead, AreaJobsRequest,
	AreaMonitoringRead, AreaAlertsHandle, AreaActivityRead,
	AreaAutomationRequest,
}

// areaPerms maps each area to its permissions. jobs.run is deliberately in
// no area: an assistant token can request a run, never start one.
var areaPerms = map[Area][]Permission{
	AreaInventoryRead:    {PermHostsRead, PermInventoryRead},
	AreaInventoryWrite:   {PermHostsRead, PermInventoryRead, PermHostsWrite, PermInventoryWrite},
	AreaReposRead:        {PermReposRead, PermPlaybooksRead},
	AreaReposWrite:       {PermReposRead, PermPlaybooksRead, PermReposWrite, PermPlaybooksWrite},
	AreaTemplatesRead:    {PermTemplatesRead},
	AreaTemplatesWrite:   {PermTemplatesRead, PermTemplatesWrite},
	AreaCredentialsNames: {PermCredentialsRead},
	AreaJobsRead:         {PermJobsRead},
	AreaJobsRequest:      {PermJobsRead, PermJobsRequest},
	AreaMonitoringRead:   {PermAgentsRead, PermChecksRead, PermMaintenanceRead},
	AreaAlertsHandle:     {PermAgentsRead, PermChecksRead, PermMaintenanceRead, PermAlertsHandle},
	AreaActivityRead:     {PermAuditRead},
	// automation.request reads requests (and the runs an approved schedule
	// starts) like jobs.request does; it grants no write on templates or
	// repositories, and approving stays a person's.
	AreaAutomationRequest: {PermJobsRead, PermAutomationRequest},
}

// Areas returns every known area in canonical order.
func Areas() []Area { return slices.Clone(areaOrder) }

// ValidArea reports whether a is a known area.
func ValidArea(a Area) bool { _, ok := areaPerms[a]; return ok }

// AreaPermissions returns a copy of the permissions area a grants.
func AreaPermissions(a Area) ([]Permission, bool) {
	p, ok := areaPerms[a]
	return slices.Clone(p), ok
}

// AreasAllow reports whether any of areas grants perm. Unknown areas grant
// nothing, so a stored value this release does not know fails closed.
func AreasAllow(areas []Area, perm Permission) bool {
	for _, a := range areas {
		if slices.Contains(areaPerms[a], perm) {
			return true
		}
	}
	return false
}

// RoleAllowsArea reports whether role holds every permission of area, so
// an area granted to a token capped at role is not dead weight.
func RoleAllowsArea(role Role, a Area) bool {
	perms, ok := areaPerms[a]
	if !ok {
		return false
	}
	for _, p := range perms {
		if !Allows(role, p) {
			return false
		}
	}
	return true
}
