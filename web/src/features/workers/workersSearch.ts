import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import type { Worker } from '../../api/types.ts';

export const WORKER_STATUSES = ['online', 'offline', 'revoked'] as const;

/**
 * The workers list's filters in the URL: the zone (its slug, as the
 * column shows it) and the status. The pending enrollments below keep
 * their own expired chip in component state.
 */
export const WORKERS_FILTERS = {
  zone: param.string(),
  status: param.enum(WORKER_STATUSES),
};

export type WorkersSearch = ListSearch & FilterSearch<typeof WORKERS_FILTERS>;

export function validateWorkersSearch(search: Record<string, unknown>): WorkersSearch {
  return { ...validateListSearch(search), ...validateFilters(WORKERS_FILTERS, search) };
}

/** The workers a list's filters leave, in their order. */
export function filterWorkers(rows: readonly Worker[], f: { zone?: string; status?: string }): Worker[] {
  return rows.filter((w) => (!f.zone || w.zone_slug === f.zone) && (!f.status || w.status === f.status));
}
