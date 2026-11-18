import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import type { Schedule } from '../../api/types.ts';

export const SCHEDULE_STATUSES = ['active', 'paused'] as const;

/**
 * The schedules list's filters in the URL: active or paused, and the one job
 * template the list is narrowed to (`?template=`, which the server applies:
 * linked from a template's row menu and the template's own pages).
 */
export const SCHEDULES_FILTERS = {
  status: param.enum(SCHEDULE_STATUSES),
  template: param.uuid(),
};

/** `id` is the schedule whose editor is open. */
export type SchedulesSearch = ListSearch & FilterSearch<typeof SCHEDULES_FILTERS> & { id?: string };

export function validateSchedulesSearch(search: Record<string, unknown>): SchedulesSearch {
  return {
    ...validateListSearch(search),
    ...validateFilters(SCHEDULES_FILTERS, search),
    id: typeof search.id === 'string' ? search.id : undefined,
  };
}

/** The schedules a list's status filter leaves (the template filter is the server's). */
export function filterSchedules(rows: readonly Schedule[], f: { status?: string }): Schedule[] {
  return rows.filter((s) => !f.status || s.paused === (f.status === 'paused'));
}
