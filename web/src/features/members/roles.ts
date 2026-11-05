import type { Messages } from '../../i18n/types.ts';
import type { Role } from '../../lib/roles.ts';

/** The options of a role picker for the given roles, labelled in the active
 * language: the value stays the API's role name. */
export function roleOptions<R extends Role>(t: Messages, roles: readonly R[]): { value: R; label: string }[] {
  return roles.map((value) => ({ value, label: t.members.roles[value] }));
}
