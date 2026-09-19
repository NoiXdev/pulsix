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
  | 'channels.read'
  | 'channels.write'
  | 'maintenance.write';

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
  'channels.read': 'operator',
  'channels.write': 'admin',
  'maintenance.write': 'operator',
};

const RANK: Record<Role, number> = { viewer: 1, operator: 2, admin: 3, owner: 4 };

/** may reports whether role may perform perm. An unknown role or
 * permission fails closed, the same fail-closed rule the server applies. */
export function may(role: string | undefined | null, perm: UiPermission): boolean {
  const r = RANK[role as Role];
  const min = MINIMUM_ROLE[perm];
  return r !== undefined && min !== undefined && r >= RANK[min];
}
