import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { Member } from '../../api/types.ts';

export const MEMBER_ROLES = ['viewer', 'operator', 'admin', 'owner'] as const;
export const MEMBER_STATUSES = ['active', 'disabled'] as const;

/** The members list's filters in the URL: a search over the name and e-mail, the role and active or disabled. */
export const MEMBERS_FILTERS = {
  q: param.string(),
  role: param.enum(MEMBER_ROLES),
  status: param.enum(MEMBER_STATUSES),
};

export type MembersSearch = ListSearch & FilterSearch<typeof MEMBERS_FILTERS>;

export function validateMembersSearch(search: Record<string, unknown>): MembersSearch {
  return { ...validateListSearch(search), ...validateFilters(MEMBERS_FILTERS, search) };
}

/** The members a list's filters leave, in their order. */
export function filterMembers(rows: readonly Member[], f: { q?: string; role?: string; status?: string }): Member[] {
  return rows.filter((m) => {
    if (!matchesText(f.q, m.display_name, m.email)) return false;
    if (f.role && m.role !== f.role) return false;
    if (f.status && !!m.disabled_at !== (f.status === 'disabled')) return false;
    return true;
  });
}
