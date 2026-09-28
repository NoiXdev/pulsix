import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { CheckDefinitionInput, MetricRange, MonitoringGroupInput } from '../types.ts';

/**
 * The check pipeline's organization-level surface (v0.3b-1, spec
 * docs/superpowers/specs/2026-09-27-v0.3b-checks-design.md, "API and
 * permissions"): check definitions, the monitoring groups a definition can
 * bind to, and the group editor's selector preview.
 *
 * Every write here invalidates BOTH lists, not just its own. A monitoring
 * group is not a bystander of the definitions: deleting one cascades the
 * definitions bound to it away (migration 00043's ON DELETE CASCADE), and
 * changing a group's selector changes which hosts a definition bound to it
 * reaches. Refreshing only the list that was written would leave the other
 * page showing rows the server no longer has.
 */
function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['checks', org] });
    void qc.invalidateQueries({ queryKey: ['monitoring-groups', org] });
  };
}

export function useChecks(org: string, enabled = true) {
  return useQuery({
    queryKey: ['checks', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/checks', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export function useCreateCheck(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: CheckDefinitionInput) => unwrap(api.POST('/api/v1/orgs/{org}/checks', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

/** useUpdateCheck replaces a definition wholesale (PUT): the body carries
 * every field, including the key and plugin a seeded row will not let
 * change — see CheckDialog, which keeps those two read-only rather than
 * sending a value the server would refuse. */
export function useUpdateCheck(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: CheckDefinitionInput) => unwrap(api.PUT('/api/v1/orgs/{org}/checks/{id}', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteCheck(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/checks/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useMonitoringGroups(org: string, enabled = true) {
  return useQuery({
    queryKey: ['monitoring-groups', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/monitoring-groups', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export function useCreateMonitoringGroup(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: MonitoringGroupInput) => unwrap(api.POST('/api/v1/orgs/{org}/monitoring-groups', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateMonitoringGroup(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: MonitoringGroupInput) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/monitoring-groups/{id}', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteMonitoringGroup(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/monitoring-groups/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/**
 * useMonitoringGroupPreview resolves a selector without saving anything.
 *
 * `selector` is null whenever there is nothing to ask — the caller passes
 * null for an empty field and only hands over a value once its own
 * debounce has settled (see MonitoringGroupsPage). The route expands the
 * expression across the organization's whole host table, so one request
 * per keystroke would be a self-inflicted load problem, and an empty
 * selector has no answer worth a round trip at all.
 *
 * retry: false, because the interesting failure is a 400 carrying the
 * parser's own message about where the expression went wrong — retrying
 * it three times only delays showing the operator what is wrong.
 */
export function useMonitoringGroupPreview(org: string, selector: string | null) {
  return useQuery({
    queryKey: ['monitoring-group-preview', org, selector],
    queryFn: () =>
      unwrap(api.POST('/api/v1/orgs/{org}/monitoring-groups/preview', { params: { path: { org } }, body: { selector: selector! } })),
    enabled: !!org && selector !== null,
    retry: false,
  });
}

/**
 * useHostChecks reads one host's effective check set — what actually runs
 * there after the org/group/host precedence has been applied, with each
 * entry's source and its current values.
 *
 * Refreshed every 30 s, the agent's own heartbeat cadence: the card's
 * whole point is the current reading, and a value that only moves on a
 * page reload is a screenshot. `monitored` rides on the envelope and is
 * deliberately independent of the set — an unmonitored host still resolves
 * to a definite list of checks, nothing runs them — so the card must read
 * the flag rather than infer the state from an empty list.
 */
export function useHostChecks(org: string, hostId: string, enabled = true) {
  return useQuery({
    queryKey: ['host-checks', org, hostId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}/checks', { params: { path: { org, id: hostId } } })),
    enabled: !!org && !!hostId && enabled,
    refetchInterval: 30_000,
  });
}

/** The tuple that names one graphable series. `item` is empty for a
 * whole-host metric (CPU, memory) and the mount or unit otherwise. */
export interface MetricSelection {
  check: string;
  item: string;
  metric: string;
  range: MetricRange;
}

/**
 * useHostMetric reads one numeric metric's series, bucketed server-side.
 *
 * `selection` is null while no graph is open, so nothing is requested
 * until an operator asks for one — these are the only reads on this page
 * that hit the sample hypertable, and a card listing a dozen metrics
 * would otherwise fire a dozen range queries on mount for graphs nobody
 * opened.
 *
 * retry: false, because the failures worth seeing here are final by
 * construction: the route answers 404 for a tuple this host has never
 * reported and 400 for a discrete metric or an unknown range. Retrying
 * either only delays the message.
 *
 * refetchInterval matches useHostChecks' cadence while the query is
 * healthy, and stops dead once it has failed. An open graph that froze at
 * the instant it was opened, while the current values above it kept
 * ticking, would be the one stale thing on a page whose whole subject is
 * staleness — but a plain `30_000` would also re-fire the failures the
 * paragraph above calls final: TanStack's interval timer calls the fetcher
 * with no error-state check, so a graph opened on a tuple the host never
 * reported would ask again twice a minute, forever, for an answer that
 * cannot change. `retry: false` does not cover this; it governs one
 * fetch's attempts, not whether the next one is scheduled.
 */
export function useHostMetric(org: string, hostId: string, selection: MetricSelection | null) {
  return useQuery({
    queryKey: ['host-metric', org, hostId, selection],
    queryFn: () => {
      const { check, item, metric, range } = selection!;
      return unwrap(
        api.GET('/api/v1/orgs/{org}/hosts/{id}/metrics', {
          // `item` is omitted, not sent empty, for a whole-host metric —
          // the route documents it as "omit for a whole-host metric", and
          // `item=` would be a second spelling of the same question.
          params: { path: { org, id: hostId }, query: { check, metric, range, ...(item ? { item } : {}) } },
        }),
      );
    },
    enabled: !!org && !!hostId && selection !== null,
    retry: false,
    refetchInterval: (query) => (query.state.error ? false : 30_000),
  });
}
