import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import type { AlertRoute, NotifyChannel } from '../../api/types.ts';

export const CHANNEL_KINDS = ['email', 'webhook'] as const;
export const ENABLED_STATES = ['enabled', 'disabled'] as const;

/** The notification channels' filters in the URL: e-mail or webhook, enabled or disabled. */
export const CHANNELS_FILTERS = {
  kind: param.enum(CHANNEL_KINDS),
  state: param.enum(ENABLED_STATES),
};

/** The alert routes' filter in the URL: enabled or disabled. */
export const ALERT_ROUTES_FILTERS = {
  state: param.enum(ENABLED_STATES),
};

export type ChannelsSearch = ListSearch & FilterSearch<typeof CHANNELS_FILTERS>;
export type AlertRoutesSearch = ListSearch & FilterSearch<typeof ALERT_ROUTES_FILTERS>;

/** An earlier link said `?status=` for enabled/disabled; it still opens the same filter. */
function withState(search: Record<string, unknown>): Record<string, unknown> {
  return search.state === undefined && search.status !== undefined ? { ...search, state: search.status } : search;
}

export function validateChannelsSearch(search: Record<string, unknown>): ChannelsSearch {
  return { ...validateListSearch(search), ...validateFilters(CHANNELS_FILTERS, withState(search)) };
}

export function validateAlertRoutesSearch(search: Record<string, unknown>): AlertRoutesSearch {
  return { ...validateListSearch(search), ...validateFilters(ALERT_ROUTES_FILTERS, withState(search)) };
}

/** The channels a list's filters leave, in their order. */
export function filterChannels(rows: readonly NotifyChannel[], f: { kind?: string; state?: string }): NotifyChannel[] {
  return rows.filter((c) => (!f.kind || c.kind === f.kind) && (!f.state || c.enabled === (f.state === 'enabled')));
}

/** The routes a list's filter leaves, in their order. */
export function filterAlertRoutes(rows: readonly AlertRoute[], f: { state?: string }): AlertRoute[] {
  return rows.filter((r) => !f.state || r.enabled === (f.state === 'enabled'));
}
