import type { Messages } from '../../i18n/types.ts';
import type { Role } from '../../lib/roles.ts';

/** The options of a role picker for the given roles, labelled in the active
 * language: the value stays the API's role name. */
export function roleOptions<R extends Role>(t: Messages, roles: readonly R[]): { value: R; label: string }[] {
  return roles.map((value) => ({ value, label: t.members.roles[value] }));
}

/** The roles from least to most privileged: what a role column sorts by. */
export const ROLE_RANK: Readonly<Record<Role, number>> = { viewer: 0, operator: 1, admin: 2, owner: 3 };

/** A role's rank for sorting; a role this build does not know sorts first. */
export function roleRank(role: string): number {
  return Object.hasOwn(ROLE_RANK, role) ? ROLE_RANK[role as Role] : -1;
}
