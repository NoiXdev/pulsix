import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import type { ItemDecision } from '../../api/types.ts';

/** The item decisions list's filters in the URL: the verdict and the check the decision governs. */
export const DECISIONS_FILTERS = {
  verdict: param.enum(['watch', 'ignore']),
  check: param.string(),
};

export type DecisionsSearch = ListSearch & FilterSearch<typeof DECISIONS_FILTERS>;

export function validateDecisionsSearch(search: Record<string, unknown>): DecisionsSearch {
  return { ...validateListSearch(search), ...validateFilters(DECISIONS_FILTERS, search) };
}

/** The decisions a list's filters leave, in their order. */
export function filterDecisions(rows: readonly ItemDecision[], f: { verdict?: string; check?: string }): ItemDecision[] {
  return rows.filter((d) => (!f.verdict || d.decision === f.verdict) && (!f.check || d.check_key === f.check));
}
