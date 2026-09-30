import { useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/**
 * FleetFilter mirrors GET /orgs/{org}/fleet's own query parameters
 * (fleet_handlers.go's fleetInput): a zone id, a monitoring group id, and a
 * single tag token. All three are plain strings, undefined meaning "this
 * filter is not set" — the same convention useHosts.ts's HostListFilter
 * uses for its own zone filter.
 */
export interface FleetFilter {
  zone?: string;
  group?: string;
  tag?: string;
}

/**
 * useFleet reads the fleet overview: every host in the organization
 * matching `filter`, worst state first, with per-severity counts and
 * totals over that same filtered set (not the organization as a whole —
 * see FleetPage, which labels the totals accordingly).
 *
 * retry: false, for the same reason useMonitoringGroupPreview and
 * useValidateRule already have it: the interesting failure here is the
 * route's own 422 when `tag` is a selector expression it does not accept
 * ("!=", "in(...)", boolean composition) rather than "!" or "()" — the API
 * rejects it and names the token in its message, and retrying that three
 * times only delays showing the operator what is wrong.
 */
export function useFleet(org: string, filter: FleetFilter = {}, enabled = true) {
  return useQuery({
    queryKey: ['fleet', org, filter],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/fleet', {
          params: { path: { org }, query: { zone: filter.zone ?? '', group: filter.group ?? '', tag: filter.tag ?? '' } },
        }),
      ),
    enabled: !!org && enabled,
    retry: false,
  });
}
