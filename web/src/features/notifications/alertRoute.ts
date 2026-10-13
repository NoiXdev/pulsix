import type { AlertRoute } from '../../api/types.ts';

/** How a route's selections are named to a reader. Shared by the two
 * views that show them — Settings → Alert routes and the host page's
 * route-union card — because a route that says "only fires on crit" in
 * one place and something else in the other is worse than either. */
export const SEVERITY_LABEL: Record<string, string> = { warn: 'Warning', crit: 'Critical', unknown: 'Unknown' };

export const EVENT_LABEL: Record<string, string> = {
  opened: 'opened',
  severity_raised: 'worsened',
  acknowledged: 'acknowledged',
  ack_voided: 'ack voided',
  snoozed: 'snoozed',
  unsnoozed: 'unsnoozed',
  closed: 'closed',
};

/** A route with no channel left reaches nobody. The API rejects creating
 * one, but deleting a channel removes the link rather than the route
 * (migration 00052's own ON DELETE CASCADE on alert_route_channels), so
 * a route can arrive here channel-less and keep looking configured while
 * being silent — the trap backlog 4.29 records for maintenance windows,
 * and the one decision 8 of the v0.4b design requires be a visible fact
 * rather than a silence. */
export function routeReachesNobody(route: AlertRoute): boolean {
  return (route.channels ?? []).length === 0;
}
