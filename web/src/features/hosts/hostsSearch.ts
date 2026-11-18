import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';

/**
 * The hosts list's URL: the list's own `?sort=&order=&page=` plus its
 * filters. `q` is the search (also what the shell's ⌘K box sets), `zone` a
 * zone id, and the three chips are present only when on. Absent means no
 * filter, so a link without them stays valid.
 */
export interface HostsSearch extends ListSearch {
  q?: string;
  zone?: string;
  managed?: true;
  monitored?: true;
  activeRuns?: true;
}

/** A chip is on for `true` (what the router's JSON parsing makes of
 * `?managed=true`) or the string "true"; anything else leaves it off. */
function on(v: unknown): true | undefined {
  return v === true || v === 'true' ? true : undefined;
}

/** A zone id as the router hands it: a numeric-looking id arrives parsed. */
function text(v: unknown): string | undefined {
  if (typeof v === 'number') return String(v);
  return typeof v === 'string' && v !== '' ? v : undefined;
}

export function validateHostsSearch(search: Record<string, unknown>): HostsSearch {
  return {
    ...validateListSearch(search),
    q: typeof search.q === 'string' && search.q !== '' ? search.q : undefined,
    zone: text(search.zone),
    managed: on(search.managed),
    monitored: on(search.monitored),
    activeRuns: on(search.activeRuns),
  };
}
