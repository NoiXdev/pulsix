import { param, validateFilters, type FilterSearch } from '../lists/filterSearch.ts';
import { validateListSearch, type ListSearch } from '../lists/listSearch.ts';
import type { AlertRoute, NotifyChannel } from '../../api/types.ts';

export const CHANNEL_KINDS = ['email', 'webhook'] as const;
export const ENABLED_STATES = ['enabled', 'disabled'] as const;

/** The notification channels' filters in the URL: e-mail or webhook, enabled or disabled. */
export const CHANNELS_FILTERS = {
  kind: param.enum(CHANNEL_KINDS),
  status: param.enum(ENABLED_STATES),
};

/** The alert routes' filter in the URL: enabled or disabled. */
export const ALERT_ROUTES_FILTERS = {
  status: param.enum(ENABLED_STATES),
};

export type ChannelsSearch = ListSearch & FilterSearch<typeof CHANNELS_FILTERS>;
export type AlertRoutesSearch = ListSearch & FilterSearch<typeof ALERT_ROUTES_FILTERS>;

export function validateChannelsSearch(search: Record<string, unknown>): ChannelsSearch {
  return { ...validateListSearch(search), ...validateFilters(CHANNELS_FILTERS, search) };
}

export function validateAlertRoutesSearch(search: Record<string, unknown>): AlertRoutesSearch {
  return { ...validateListSearch(search), ...validateFilters(ALERT_ROUTES_FILTERS, search) };
}

/** The channels a list's filters leave, in their order. */
export function filterChannels(rows: readonly NotifyChannel[], f: { kind?: string; status?: string }): NotifyChannel[] {
  return rows.filter((c) => (!f.kind || c.kind === f.kind) && (!f.status || c.enabled === (f.status === 'enabled')));
}

/** The routes a list's filter leaves, in their order. */
export function filterAlertRoutes(rows: readonly AlertRoute[], f: { status?: string }): AlertRoute[] {
  return rows.filter((r) => !f.status || r.enabled === (f.status === 'enabled'));
}
