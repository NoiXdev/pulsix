import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { AdminOrg } from '../../api/types.ts';

/** The organizations list's filter in the URL: a search over the name and the slug. */
export const ORGANIZATIONS_FILTERS = {
  q: param.string(),
};

export type OrganizationsSearch = ListSearch & FilterSearch<typeof ORGANIZATIONS_FILTERS>;

export function validateOrganizationsSearch(search: Record<string, unknown>): OrganizationsSearch {
  return { ...validateListSearch(search), ...validateFilters(ORGANIZATIONS_FILTERS, search) };
}

/** The organizations a list's filters leave, in their order. */
export function filterOrganizations(rows: readonly AdminOrg[], f: { q?: string }): AdminOrg[] {
  return rows.filter((o) => matchesText(f.q, o.name, o.slug));
}
