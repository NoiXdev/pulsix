import type { Messages } from './types.ts';

/** A role's name in the active language. The API speaks the role keys
 * (viewer, operator, admin, owner); a key this catalogue does not know is
 * shown as it is. */
export function roleLabel(t: Messages, role: string): string {
  return (t.members.roles as Record<string, string>)[role] ?? role;
}
