import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import { OBJECT_TYPES } from './objectTypes.ts';

/** How far back the activity feeds can look: the API takes a `since` instant, so a range is a start only. */
export const SINCE_RANGES = ['1d', '7d', '30d'] as const;
export type SinceRange = (typeof SINCE_RANGES)[number];

const DAYS: Record<SinceRange, number> = { '1d': 1, '7d': 7, '30d': 30 };

/** The RFC 3339 instant a range starts at, to the minute so the request stays the same while the page is open. */
export function sinceOf(range: SinceRange | undefined, now = new Date()): string | undefined {
  if (!range) return undefined;
  const start = new Date(now.getTime() - DAYS[range] * 86_400_000);
  start.setSeconds(0, 0);
  return start.toISOString();
}

/**
 * The organization activity feed's filters in the URL: the member who acted
 * (`actor`, a user id), the object type (`type`) and how far back to look
 * (`since`). The server applies all three; the feed is server-paged.
 */
export const ACTIVITY_FILTERS = {
  actor: param.string(),
  type: param.enum(OBJECT_TYPES),
  since: param.enum(SINCE_RANGES),
};

/**
 * My activity's: the object type and `since`. The API has no organization
 * filter on /me/activity, so none is offered.
 */
export const MY_ACTIVITY_FILTERS = {
  type: ACTIVITY_FILTERS.type,
  since: ACTIVITY_FILTERS.since,
};

export type ActivitySearch = ListSearch & FilterSearch<typeof ACTIVITY_FILTERS>;
export type MyActivitySearch = ListSearch & FilterSearch<typeof MY_ACTIVITY_FILTERS>;

export function validateActivitySearch(search: Record<string, unknown>): ActivitySearch {
  return { ...validateListSearch(search), ...validateFilters(ACTIVITY_FILTERS, search) };
}

export function validateMyActivitySearch(search: Record<string, unknown>): MyActivitySearch {
  return { ...validateListSearch(search), ...validateFilters(MY_ACTIVITY_FILTERS, search) };
}
