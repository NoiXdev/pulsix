import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { matchesText } from '../lists/textMatch.ts';
import type { MaintenanceWindow } from '../../api/types.ts';

/** The status the maintenance list's filter offers. */
export const WINDOW_STATUSES = ['active', 'upcoming', 'past'] as const;
export type WindowStatus = (typeof WINDOW_STATUSES)[number];

/** The maintenance list's filters in the URL: a search over the name, note and target, and the window's status. */
export const MAINTENANCE_FILTERS = {
  q: param.string(),
  status: param.enum(WINDOW_STATUSES),
};

export type MaintenanceSearch = ListSearch & FilterSearch<typeof MAINTENANCE_FILTERS>;

export function validateMaintenanceSearch(search: Record<string, unknown>): MaintenanceSearch {
  return { ...validateListSearch(search), ...validateFilters(MAINTENANCE_FILTERS, search) };
}

/**
 * Where a window stands at `now`: active (the server's active_now), past (a
 * one-off window that has ended) or upcoming (everything else: a one-off
 * window that has not started and a recurring one between its occurrences).
 */
export function windowStatus(w: Pick<MaintenanceWindow, 'active_now' | 'cron' | 'ends_at'>, now: Date): WindowStatus {
  if (w.active_now) return 'active';
  if (!w.cron && w.ends_at && new Date(w.ends_at).getTime() <= now.getTime()) return 'past';
  return 'upcoming';
}

/** The windows a list's filters leave, in their order. */
export function filterWindows(rows: readonly MaintenanceWindow[], f: { q?: string; status?: string }, now: Date): MaintenanceWindow[] {
  return rows.filter((w) => matchesText(f.q, w.name, w.note, w.target_name) && (!f.status || windowStatus(w, now) === f.status));
}
