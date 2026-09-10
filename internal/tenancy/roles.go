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
)

// rank orders roles from least to most privileged. A role absent from this
// map (an unknown or invalid Role value) has rank 0, lower than every known
// role — see Allows and Lower for why that matters.
var rank = map[Role]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3, RoleOwner: 4}

// matrix is the fixed, minimum-role-per-permission table: owner can do
// everything, admin everything except deleting the org, operator can read
// and write hosts and read the audit log, viewer can only read hosts.
var matrix = map[Permission]Role{
	PermOrgManage:     RoleAdmin,
	PermOrgDelete:     RoleOwner,
	PermMembersManage: RoleAdmin,
	PermTokensManage:  RoleAdmin,
	PermHostsRead:     RoleViewer,
	PermHostsWrite:    RoleOperator,
	PermAuditRead:     RoleOperator,
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
