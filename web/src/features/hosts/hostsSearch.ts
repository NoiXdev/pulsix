import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';

/**
 * The hosts list's filters in the URL. `q` is the search (also what the
 * shell's ⌘K box sets), `zone` a zone id (only a UUID-shaped string: a
 * hand-typed value means no filter), and the three chips are present only
 * when on. Absent means no filter, so a link without them stays valid.
 */
export const HOSTS_FILTERS = {
  q: param.string(),
  zone: param.uuid(),
  managed: param.flag(),
  monitored: param.flag(),
  activeRuns: param.flag(),
};

/** The hosts list's URL: the list's own `?sort=&order=&page=` plus its filters. */
export type HostsSearch = ListSearch & FilterSearch<typeof HOSTS_FILTERS>;

export function validateHostsSearch(search: Record<string, unknown>): HostsSearch {
  return { ...validateListSearch(search), ...validateFilters(HOSTS_FILTERS, search) };
}
