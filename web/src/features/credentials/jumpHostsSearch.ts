import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { Bastion } from '../../api/types.ts';
import { bastionKeyState } from './bastionKeyState.ts';

/** The host key states the key filter offers (what bastionKeyState reads). */
export const BASTION_KEY_STATES = ['pinned', 'unpinned', 'changed'] as const;

/**
 * The jump hosts list's filters in the URL: a search over the name, the address and the
 * login, the host key's state (a jump host has no zone, and no reachability
 * beyond what its last connection test saw of the key) and the credential
 * it logs in with.
 */
export const JUMP_HOST_FILTERS = {
  q: param.string(),
  key: param.enum(BASTION_KEY_STATES),
  credential: param.string(),
};

export type JumpHostsSearch = ListSearch & FilterSearch<typeof JUMP_HOST_FILTERS>;

export function validateJumpHostsSearch(search: Record<string, unknown>): JumpHostsSearch {
  return { ...validateListSearch(search), ...validateFilters(JUMP_HOST_FILTERS, search) };
}

/** The jump hosts a list's filters leave, in their order. */
export function filterBastions(rows: readonly Bastion[], f: { q?: string; key?: string; credential?: string }): Bastion[] {
  return rows.filter((b) => {
    if (!matchesText(f.q, b.name, b.address, b.username)) return false;
    if (f.key && bastionKeyState(b) !== f.key) return false;
    if (f.credential && b.credential_id !== f.credential) return false;
    return true;
  });
}
