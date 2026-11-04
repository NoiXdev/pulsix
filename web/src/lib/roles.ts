/**
 * The SPA-side mirror of the server's permission table
 * (internal/tenancy/roles.go): the session carries no permission list, but
 * every org response carries the caller's role, and the role→permission
 * matrix is a fixed, published shape — mirroring the minimum-role table
 * here keeps write affordances off the page for the roles the server would
 * refuse anyway, without inventing a second source of truth for the
 * *server's* answers (a 403 is still the gate; this only decides what the
 * UI offers).
 */
export type Role = 'owner' | 'admin' | 'operator' | 'viewer';

export type UiPermission =
  | 'hosts.write'
  | 'inventory.write'
  | 'credentials.read'
  | 'credentials.write'
  | 'repos.write'
  | 'playbooks.write'
  | 'templates.write'
  | 'jobs.run'
  | 'workers.read'
  | 'workers.write'
  | 'agents.read'
  | 'agents.write'
  | 'checks.read'
  | 'checks.write'
  | 'channels.read'
  | 'channels.write'
  | 'maintenance.write'
  | 'org.manage'
  | 'members.manage';

/** Minimum role per permission, exactly internal/tenancy/roles.go's matrix:
 * operators drive hosts, inventory, repos, playbooks, templates and runs
 * (and may read credentials); credentials are an admin write. Channels
 * (spec §5): operators read them and their delivery log, admins manage
 * them. */
const MINIMUM_ROLE: Record<UiPermission, Role> = {
  'hosts.write': 'operator',
  'inventory.write': 'operator',
  'credentials.read': 'operator',
  'credentials.write': 'admin',
  'repos.write': 'operator',
  'playbooks.write': 'operator',
  'templates.write': 'operator',
  'jobs.run': 'operator',
  'workers.read': 'admin',
  'workers.write': 'admin',
  // Agents (v0.3a) follow hosts, not workers: an agent belongs to one
  // host, so a viewer sees which hosts report and an operator enrols and
  // revokes them.
  'agents.read': 'viewer',
  'agents.write': 'operator',
  // Monitoring checks (v0.3b-1) mirror agents: what is measured on a host
  // is part of operating it, so a viewer reads the definitions, groups and
  // graphs and an operator edits them.
  'checks.read': 'viewer',
  'checks.write': 'operator',
  'channels.read': 'operator',
  'channels.write': 'admin',
  'maintenance.write': 'operator',
  // Renaming the organization (internal/tenancy/roles.go PermOrgManage).
  'org.manage': 'admin',
  // Changing a member's role or removing a member (PermMembersManage). The
  // owner rules on top (only an owner touches an owner, the last owner
  // stays) are the server's.
  'members.manage': 'admin',
};

const RANK: Record<Role, number> = { viewer: 1, operator: 2, admin: 3, owner: 4 };

/** may reports whether role may perform perm. An unknown role or
 * permission fails closed, the same fail-closed rule the server applies. */
export function may(role: string | undefined | null, perm: UiPermission): boolean {
  const r = RANK[role as Role];
  const min = MINIMUM_ROLE[perm];
  return r !== undefined && min !== undefined && r >= RANK[min];
}
