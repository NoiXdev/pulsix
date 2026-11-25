import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import type { RunRequest } from '../../api/hooks/useRunRequests.ts';

export const REQUEST_STATUSES = ['pending', 'waiting_for_sync', 'approved', 'rejected', 'expired', 'failed'] as const;
export const REQUEST_KINDS = ['run', 'retry_failed', 'schedule', 'trigger', 'schedule_review', 'trigger_review'] as const;

/** The run requests list's filters in the URL: status, kind and the job template asked for. */
export const RUN_REQUESTS_FILTERS = {
  status: param.enum(REQUEST_STATUSES),
  kind: param.enum(REQUEST_KINDS),
  template: param.string(),
};

export type RunRequestsSearch = ListSearch & FilterSearch<typeof RUN_REQUESTS_FILTERS>;

export function validateRunRequestsSearch(search: Record<string, unknown>): RunRequestsSearch {
  return { ...validateListSearch(search), ...validateFilters(RUN_REQUESTS_FILTERS, search) };
}

/** The requests a list's filters leave, in their order. */
export function filterRunRequests(rows: readonly RunRequest[], f: { status?: string; kind?: string; template?: string }): RunRequest[] {
  return rows.filter((q) => {
    if (f.status && q.status !== f.status) return false;
    if (f.kind && q.kind !== f.kind) return false;
    if (f.template && q.template_id !== f.template) return false;
    return true;
  });
}
