import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { OrgSecret } from '../../api/types.ts';

/** The secret store's filters in the URL: a search over the name, the field names and the checks using it, and the unused chip. */
export const SECRETS_FILTERS = {
  q: param.string(),
  unused: param.flag(),
};

export type SecretsSearch = ListSearch & FilterSearch<typeof SECRETS_FILTERS>;

export function validateSecretsSearch(search: Record<string, unknown>): SecretsSearch {
  return { ...validateListSearch(search), ...validateFilters(SECRETS_FILTERS, search) };
}

/** The secrets a list's filters leave, in their order. */
export function filterSecrets(rows: readonly OrgSecret[], f: { q?: string; unused?: true }): OrgSecret[] {
  return rows.filter((s) => {
    if (!matchesText(f.q, s.name, ...(s.field_names ?? []), ...(s.used_by ?? []))) return false;
    if (f.unused && (s.used_by ?? []).length > 0) return false;
    return true;
  });
}
