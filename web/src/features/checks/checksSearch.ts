import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { CheckDefinition } from '../../api/types.ts';

/**
 * The check definitions list's filters in the URL: a search over the key and
 * the plugin, one plugin (named as the rows name it) and enabled or
 * disabled.
 */
export const CHECKS_FILTERS = {
  q: param.string(),
  plugin: param.string(),
  state: param.enum(['enabled', 'disabled']),
};

export type ChecksSearch = ListSearch & FilterSearch<typeof CHECKS_FILTERS>;

export function validateChecksSearch(search: Record<string, unknown>): ChecksSearch {
  return { ...validateListSearch(search), ...validateFilters(CHECKS_FILTERS, search) };
}

/** The definitions a list's filters leave, in their order. */
export function filterChecks(rows: readonly CheckDefinition[], f: { q?: string; plugin?: string; state?: string }): CheckDefinition[] {
  return rows.filter((c) => {
    if (!matchesText(f.q, c.key, c.plugin)) return false;
    if (f.plugin && c.plugin !== f.plugin) return false;
    if (f.state && c.enabled !== (f.state === 'enabled')) return false;
    return true;
  });
}
