import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { Rule } from '../../api/types.ts';

/** The rules list's filters in the URL: a search over the name and the metric, the severity and enabled or disabled. */
export const RULES_FILTERS = {
  q: param.string(),
  severity: param.enum(['warn', 'crit']),
  state: param.enum(['enabled', 'disabled']),
};

export type RulesSearch = ListSearch & FilterSearch<typeof RULES_FILTERS>;

export function validateRulesSearch(search: Record<string, unknown>): RulesSearch {
  return { ...validateListSearch(search), ...validateFilters(RULES_FILTERS, search) };
}

/** The rules a list's filters leave, in their order. */
export function filterRules(rows: readonly Rule[], f: { q?: string; severity?: string; state?: string }): Rule[] {
  return rows.filter((r) => {
    if (!matchesText(f.q, r.name, r.metric)) return false;
    if (f.severity && r.severity !== f.severity) return false;
    if (f.state && r.enabled !== (f.state === 'enabled')) return false;
    return true;
  });
}
