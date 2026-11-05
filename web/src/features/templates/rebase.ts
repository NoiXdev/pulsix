/** The three-way merge the template dialog's Reload applies after a 412:
 * a setting the person changed against the base the form was loaded from
 * keeps their value, every other one takes the template's current value. */
import { sameValue } from '../variables/origin.ts';
import { identitiesForBody, type VaultIdentityRow } from '../../lib/vaultIdentity.ts';
import type { Scope } from './ScopePicker.tsx';

/** mergeSetting is draft when it differs from base (by same), else now. */
export function mergeSetting<T>(draft: T, base: T, now: T, same: (a: T, b: T) => boolean = sameValue): T {
  return same(draft, base) ? now : draft;
}

/** sameScope compares two scopes with their hosts as a set: ticking a
 * host off and on again moves it to the end, which is no change. */
function sameScope(a: Scope, b: Scope): boolean {
  return a.group_id === b.group_id && sameValue([...a.host_ids].sort(), [...b.host_ids].sort());
}

export function mergeScope(draft: Scope, base: Scope, now: Scope): Scope {
  return mergeSetting(draft, base, now, sameScope);
}

/** mergeVaultRows compares the identities the rows send, not the rows:
 * a row's key is the form's own and differs on every load. */
export function mergeVaultRows(draft: VaultIdentityRow[], base: VaultIdentityRow[], now: VaultIdentityRow[]): VaultIdentityRow[] {
  return mergeSetting(draft, base, now, (a, b) => sameValue(identitiesForBody(a), identitiesForBody(b)));
}
