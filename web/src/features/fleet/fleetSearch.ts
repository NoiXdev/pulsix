import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { FleetHost } from '../../api/types.ts';

/**
 * The fleet overview's filters in the URL. `zone`, `group` and `tag` are
 * what GET /orgs/{org}/fleet filters by (the totals above the list follow
 * them); `q` (a host name) and `state` narrow the answer in the browser.
 */
export const FLEET_FILTERS = {
  q: param.string(),
  zone: param.uuid(),
  group: param.uuid(),
  tag: param.string(),
  state: param.enum(['crit', 'warn', 'ok', 'unknown']),
};

export type FleetSearch = ListSearch & FilterSearch<typeof FLEET_FILTERS>;

export function validateFleetSearch(search: Record<string, unknown>): FleetSearch {
  return { ...validateListSearch(search), ...validateFilters(FLEET_FILTERS, search) };
}

/** The hosts the browser-side filters (search, state) leave, in their order. */
export function filterFleetHosts(rows: readonly FleetHost[], f: { q?: string; state?: string }): FleetHost[] {
  return rows.filter((h) => matchesText(f.q, h.name) && (!f.state || h.state === f.state));
}

/**
 * The severity order of a host's state, worst first: critical, warn,
 * unknown, ok, then the hosts with no roll-up (no data, all ignored). The
 * same order GET /orgs/{org}/fleet answers in; the State column sorts by it.
 */
export function fleetStateRank(h: Pick<FleetHost, 'state' | 'all_ignored'>): number {
  switch (h.state) {
    case 'crit':
      return 0;
    case 'warn':
      return 1;
    case 'unknown':
      return 2;
    case 'ok':
      return 3;
    default:
      return h.all_ignored ? 5 : 4;
  }
}
