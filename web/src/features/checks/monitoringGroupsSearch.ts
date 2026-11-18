import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { MonitoringGroup } from '../../api/types.ts';

/** The monitoring groups list's filter in the URL: a search over the name and the selector. */
export const MONITORING_GROUPS_FILTERS = {
  q: param.string(),
};

export type MonitoringGroupsSearch = ListSearch & FilterSearch<typeof MONITORING_GROUPS_FILTERS>;

export function validateMonitoringGroupsSearch(search: Record<string, unknown>): MonitoringGroupsSearch {
  return { ...validateListSearch(search), ...validateFilters(MONITORING_GROUPS_FILTERS, search) };
}

/** The groups a list's filters leave, in their order. */
export function filterMonitoringGroups(rows: readonly MonitoringGroup[], f: { q?: string }): MonitoringGroup[] {
  return rows.filter((g) => matchesText(f.q, g.name, g.selector));
}
