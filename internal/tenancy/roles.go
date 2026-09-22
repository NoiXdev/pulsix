// Package tenancy implements organizations, memberships, roles and invitations.
package tenancy

// Role is an org membership role.
type Role string

// Roles from most to least privileged.
const (
	RoleOwner    Role = "owner"
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// Permission names an action checked by the API layer.
type Permission string

// Permissions used in v0.1.
const (
	PermOrgManage     Permission = "org.manage"
	PermOrgDelete     Permission = "org.delete"
	PermMembersManage Permission = "members.manage"
	PermTokensManage  Permission = "tokens.manage"
	PermHostsRead     Permission = "hosts.read"
	PermHostsWrite    Permission = "hosts.write"
	PermAuditRead     Permission = "audit.read"

	PermInventoryRead    Permission = "inventory.read"
	PermInventoryWrite   Permission = "inventory.write"
	PermCredentialsRead  Permission = "credentials.read"
	PermCredentialsWrite Permission = "credentials.write"

	PermReposRead      Permission = "repos.read"
	PermReposWrite     Permission = "repos.write"
	PermPlaybooksRead  Permission = "playbooks.read"
	PermPlaybooksWrite Permission = "playbooks.write"

	PermTemplatesRead  Permission = "templates.read"
	PermTemplatesWrite Permission = "templates.write"

	// Jobs is the run view (backlog 2.12): reading runs, their events and
	// their live stream needs no more than reading the hosts and
	// templates involved; triggering a run, cancelling one and retrying
	// its failed hosts are operator actions, the same bar
	// templates.write clears.
	PermJobsRead Permission = "jobs.read"
	PermJobsRun  Permission = "jobs.run"

	PermWorkersRead  Permission = "workers.read"
	PermWorkersWrite Permission = "workers.write"

	PermChannelsRead  Permission = "channels.read"
	PermChannelsWrite Permission = "channels.write"

	PermMaintenanceRead  Permission = "maintenance.read"
	PermMaintenanceWrite Permission = "maintenance.write"

	// Org settings (backlog 2.44): everyone in the organization may read
	// its job settings; only an admin changes them, the same bar
	// org.manage clears.
	PermOrgSettingsRead  Permission = "org.settings.read"
	PermOrgSettingsWrite Permission = "org.settings.write"
)

// rank orders roles from least to most privileged. A role absent from this
// map (an unknown or invalid Role value) has rank 0, lower than every known
// role — see Allows and Lower for why that matters.
var rank = map[Role]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3, RoleOwner: 4}

// matrix is the fixed, minimum-role-per-permission table: owner can do
// everything, admin everything except deleting the org, operator can read
// and write hosts, read and write inventory, read the audit log, read
// credentials, manage git repositories, playbooks and job templates,
// viewer can only read. Writing a credential (creating, rotating or
// deleting one) needs admin.
var matrix = map[Permission]Role{
	PermOrgManage:        RoleAdmin,
	PermOrgDelete:        RoleOwner,
	PermMembersManage:    RoleAdmin,
	PermTokensManage:     RoleAdmin,
	PermHostsRead:        RoleViewer,
	PermHostsWrite:       RoleOperator,
	PermAuditRead:        RoleOperator,
	PermInventoryRead:    RoleViewer,
	PermInventoryWrite:   RoleOperator,
	PermCredentialsRead:  RoleOperator,
	PermCredentialsWrite: RoleAdmin,
	PermReposRead:        RoleViewer,
	PermReposWrite:       RoleOperator,
	PermPlaybooksRead:    RoleViewer,
	PermPlaybooksWrite:   RoleOperator,
	// Job templates sit between playbooks and jobs: reading one needs no
	// more than a playbook run configuration reveals (the plan does not
	// name a template permission — templates.read/write fill that gap,
	// following the repos/playbooks shape).
	PermTemplatesRead:  RoleViewer,
	PermTemplatesWrite: RoleOperator,
	// Runs are read-only for a viewer and operator-drivable, mirroring
	// templates: the run view shows what a run did, and operators trigger,
	// cancel and retry runs.
	PermJobsRead: RoleViewer,
	PermJobsRun:  RoleOperator,
	// Workers are infrastructure: owner and admin see and manage them
	// (spec 2026-09-18 §4).
	PermWorkersRead:  RoleAdmin,
	PermWorkersWrite: RoleAdmin,
	// Notification channels (spec 2026-09-18 §5): operators see them —
	// they pick channels for a schedule's rules and read the delivery log —
	// and admins manage them, because a channel decides where the
	// organization's information leaves Pulsix.
	PermChannelsRead:  RoleOperator,
	PermChannelsWrite: RoleAdmin,
	// Maintenance windows (§6) are part of operating hosts: everyone sees
	// them, operators plan them.
	PermMaintenanceRead:  RoleViewer,
	PermMaintenanceWrite: RoleOperator,
	// Org settings (backlog 2.44): a viewer reads the job settings (a
	// refusal message names the requirement, so it must be visible); an
	// admin changes them, since the toggle decides what jobs may run
	// against at all.
	PermOrgSettingsRead:  RoleViewer,
	PermOrgSettingsWrite: RoleAdmin,
}

// Allows reports whether role may perform perm. It is total: an unknown role
// or an unknown permission answers false rather than panicking, so a typo'd
// permission constant fails closed instead of granting access to everyone
// (a zero-value lookup in rank would otherwise compare 0 >= 0 and succeed).
func Allows(role Role, perm Permission) bool {
	r, roleKnown := rank[role]
	min, permKnown := matrix[perm]
	return roleKnown && permKnown && r >= rank[min]
}

// Lower returns the less privileged of two roles. An unknown role ranks
// lowest, so Lower is always safe to call with attacker- or
// caller-controlled input on either side.
func Lower(a, b Role) Role {
	if rank[a] <= rank[b] {
		return a
	}
	return b
}

// Valid reports whether r is a known role.
func Valid(r Role) bool { _, ok := rank[r]; return ok }
