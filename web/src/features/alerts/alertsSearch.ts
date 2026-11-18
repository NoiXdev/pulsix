import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';

/**
 * The alerts list's filters in the URL: every query parameter
 * GET /orgs/{org}/alerts accepts. `host` is HostAlertsCard's link to one
 * host's alerts (its closed ones included), shown as a removable pill.
 * `acknowledged` and `muted` are yes, no or absent (any).
 */
export const ALERTS_FILTERS = {
  state: param.enum(['open', 'closed']),
  severity: param.enum(['warn', 'crit', 'unknown']),
  scope: param.enum(['tuple', 'host']),
  acknowledged: param.bool(),
  muted: param.bool(),
  host: param.uuid(),
};

export type AlertsSearch = ListSearch & FilterSearch<typeof ALERTS_FILTERS>;

export function validateAlertsSearch(search: Record<string, unknown>): AlertsSearch {
  return { ...validateListSearch(search), ...validateFilters(ALERTS_FILTERS, search) };
}
