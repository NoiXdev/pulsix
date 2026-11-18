import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { TRIGGERS } from './trigger.ts';

/** The run statuses the status filter offers, after `active` (the live runs). */
export const RUN_STATUSES = ['queued', 'running', 'succeeded', 'failed', 'cancelled', 'error'] as const;
/** The job kinds the kind filter offers. */
export const RUN_KINDS = ['run', 'connection_test', 'bootstrap', 'rotate_key', 'revoke_key', 'sync_repo'] as const;

/**
 * The runs list's filters in the URL (?status=&kind=&trigger=&schedule=&host=).
 * `status=active` asks for the live runs; `host` is the one host the list
 * is narrowed to (linked from the host view and the hosts list's Active
 * runs). A value outside the offered ones is dropped.
 */
export const RUNS_FILTERS = {
  status: param.enum(['active', ...RUN_STATUSES]),
  kind: param.enum(RUN_KINDS),
  trigger: param.enum(TRIGGERS),
  schedule: param.string(),
  host: param.string(),
};

export type RunsSearch = ListSearch & FilterSearch<typeof RUNS_FILTERS>;

export function validateRunsSearch(search: Record<string, unknown>): RunsSearch {
  return { ...validateListSearch(search), ...validateFilters(RUNS_FILTERS, search) };
}

/**
 * The runs list's search for one host's live runs (`?host=<id>&status=active`):
 * where the hosts list's Active runs cell and its row menu lead.
 */
export function liveRunsOf(hostId: string) {
  return { host: hostId, status: 'active' as const, kind: undefined, trigger: undefined, schedule: undefined };
}
