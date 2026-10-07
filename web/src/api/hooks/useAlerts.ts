import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { AlertAcknowledgeInput, AlertSnoozeInput } from '../types.ts';

/**
 * AlertFilter mirrors GET /orgs/{org}/alerts' own query parameters
 * (alert_handlers.go's alertListInput): every field narrows, none widens,
 * and the zero filter matches every alert in the organization — the same
 * "undefined means not set" convention useFleet.ts's FleetFilter uses for
 * its own query. `host` exists on the route for the host page's own
 * filtered view (v0.4a design, "the host view is a filter on this list,
 * not its own endpoint") — this page's own AlertFilters does not offer it.
 */
export interface AlertFilter {
  state?: 'open' | 'closed';
  severity?: 'warn' | 'crit' | 'unknown';
  host?: string;
  scope?: 'tuple' | 'host';
  acknowledged?: boolean;
  muted?: boolean;
}

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => void qc.invalidateQueries({ queryKey: ['alerts', org] });
}

/**
 * useAlerts reads the organization's alerts matching `filter`, newest
 * opened first (the server's own order — this page does not re-sort, the
 * same discipline FleetPage already holds).
 */
export function useAlerts(org: string, filter: AlertFilter = {}, enabled = true) {
  return useQuery({
    queryKey: ['alerts', org, filter],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/alerts', {
          params: {
            path: { org },
            query: {
              state: filter.state ?? '',
              severity: filter.severity ?? '',
              host: filter.host ?? '',
              scope: filter.scope ?? '',
              acknowledged: filter.acknowledged === undefined ? '' : String(filter.acknowledged),
              muted: filter.muted === undefined ? '' : String(filter.muted),
            },
          },
        }),
      ),
    enabled: !!org && enabled,
  });
}

/**
 * useAlert reads one alert with its timeline (v0.4a design, "GUI" — "the
 * full story of one alert"): GET /alerts/{id}, not a second list request
 * filtered to one id — the detail route is the one place the timeline
 * comes from at all.
 */
export function useAlert(org: string, id: string, enabled = true) {
  return useQuery({
    queryKey: ['alert', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/alerts/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id && enabled,
  });
}

/** useAcknowledgeAlert records that the caller knows about an alert — never
 * a mute (Suppression is untouched, design doc's own "acknowledging is not
 * muting"). */
export function useAcknowledgeAlert(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: AlertAcknowledgeInput }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/alerts/{id}/acknowledge', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useUnacknowledgeAlert(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/alerts/{id}/acknowledge', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** useSnoozeAlert mutes one alert until an instant or until its severity
 * next changes — Service.Snooze's own "never both, never neither" is
 * enforced there; SnoozeDialog enforces it again client-side so Save never
 * sends a combination the server would 422 on. */
export function useSnoozeAlert(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: AlertSnoozeInput }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/alerts/{id}/snooze', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useUnsnoozeAlert(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/alerts/{id}/snooze', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}
