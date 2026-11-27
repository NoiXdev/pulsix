import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, unwrap } from '../client.ts';
import type { components } from '../schema.d.ts';
import { countRouteMemory } from './countRouteMemory.ts';

export type RunRequest = components['schemas']['RunRequestDTO'];
export type RunRequestStatus = RunRequest['status'];
export type RunPicture = components['schemas']['Picture'];

/** How often the navigation's pending count is refreshed, the cadence the
 * other polled lists (workers, agents) use. */
export const PENDING_POLL_MS = 30_000;

/** useRunRequests lists the organization's run requests (jobs.read), all of
 * them or those of one status. "expired" is computed by the server: a
 * pending request past its expiry. */
export function useRunRequests(org: string, status?: RunRequestStatus) {
  return useQuery({
    queryKey: ['run-requests', org, status ?? 'all'],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/run-requests', {
          params: { path: { org }, query: status ? { status } : {} },
        }),
      ),
    enabled: !!org,
  });
}

/** How long a missing count route is remembered before it is asked
 * again (a manager upgraded in the meantime then gets its exact count). */
export const COUNT_ROUTE_RETRY_MS = 10 * 60_000;

/** countPendingList counts the pending list instead, for a manager without
 * the count route. An older manager filters that list in memory after its
 * 500-row cap, so the number can undercount. */
async function countPendingList(org: string) {
  const { requests } = await unwrap(
    api.GET('/api/v1/orgs/{org}/run-requests', { params: { path: { org }, query: { status: 'pending' } } }),
  );
  return { count: requests?.length ?? 0 };
}

/** usePendingRunRequestCount is the number the navigation shows: the
 * pending requests as the server counts them (not the length of a list it
 * caps), polled so a request an assistant makes shows up without a reload.
 * Under the run-requests key, so a decision refreshes it with the lists. */
export function usePendingRunRequestCount(org: string, opts: { enabled?: boolean } = {}) {
  const query = useQuery({
    queryKey: ['run-requests', org, 'pending-count'],
    queryFn: async () => {
      const missingSince = countRouteMemory.get(org);
      if (missingSince !== undefined && Date.now() - missingSince < COUNT_ROUTE_RETRY_MS) return countPendingList(org);
      try {
        const result = await unwrap(api.GET('/api/v1/orgs/{org}/run-requests/pending-count', { params: { path: { org } } }));
        countRouteMemory.delete(org);
        return result;
      } catch (err) {
        if (!(err instanceof ApiError) || (err.status !== 404 && err.status !== 400)) throw err;
        // A manager from before the count route: 404 where the route is
        // unknown, 400 on the released rc.3 to rc.5 managers, where the path
        // lands on /run-requests/{id} and fails its UUID parsing. Remember
        // it for a while so the polls go straight to the list instead of
        // asking a route that is not there every 30 seconds.
        countRouteMemory.set(org, Date.now());
        return countPendingList(org);
      }
    },
    enabled: !!org && (opts.enabled ?? true),
    refetchInterval: PENDING_POLL_MS,
  });
  return query.data?.count;
}

/** How often an open request waiting for its repository's sync is read
 * again, so the page turns pending (or failed) on its own once the sync has
 * finished (spec 2026-11-25 A, sync_first). */
export const WAITING_POLL_MS = 5_000;

/** waitsForSync reports a request that a sync holds up: one waiting for its
 * own sync, or a pending one whose approval waits for a queued or running
 * sync of its repository. Either moves on by itself once the sync ends. */
export function waitsForSync(data: { request: { status: string }; picture: RunPicture } | undefined): boolean {
  if (!data) return false;
  if (data.request.status === 'waiting_for_sync') return true;
  return data.request.status === 'pending' && (data.picture.commit?.blocked ?? []).includes('sync_running');
}

/** useRunRequest is one request with its run picture (what the run would use
 * if it were approved now) and the fingerprint over that picture, which an
 * approval sends back. While a sync holds the request up (waitsForSync) it is
 * polled every WAITING_POLL_MS; the polling stops once nothing does. */
export function useRunRequest(org: string, id: string) {
  return useQuery({
    queryKey: ['run-request', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/run-requests/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
    refetchInterval: (query) => (waitsForSync(query.state.data) ? WAITING_POLL_MS : false),
  });
}

function useInvalidateRunRequests(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['run-requests', org] });
    void qc.invalidateQueries({ queryKey: ['run-request', org] });
    void qc.invalidateQueries({ queryKey: ['jobs', org] });
  };
}

/** useApproveRunRequest approves a request (jobs.run, browser session) with
 * the fingerprint of the picture the page showed; the server refuses (409)
 * when the picture has changed since, when the request expired or when it
 * was already decided. followUps names the direct follow-ups to start; left
 * out, the server applies its default (all startable, or none). An approved
 * trigger request's answer can carry the trigger's secret, so the mutation
 * keeps nothing in the cache (gcTime 0), like the trigger dialog's own. */
export function useApproveRunRequest(org: string, id: string) {
  const invalidate = useInvalidateRunRequests(org);
  return useMutation({
    mutationFn: ({ fingerprint, followUps }: { fingerprint: string; followUps?: string[] }) =>
      unwrap(
        api.POST('/api/v1/orgs/{org}/run-requests/{id}/approve', {
          params: { path: { org, id } },
          body: followUps === undefined ? { fingerprint } : { fingerprint, follow_ups: followUps },
        }),
      ),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

/** useRejectRunRequest rejects a pending request (jobs.run, browser session). */
export function useRejectRunRequest(org: string, id: string) {
  const invalidate = useInvalidateRunRequests(org);
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/orgs/{org}/run-requests/{id}/reject', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** firstJobOfRunGroup is a job of the run group an approval started (one
 * job per zone involved), for the page to open; undefined when the list
 * has none yet. */
export async function firstJobOfRunGroup(org: string, runGroupId: string): Promise<string | undefined> {
  const body = await unwrap(
    api.GET('/api/v1/orgs/{org}/jobs', { params: { path: { org }, query: { group: runGroupId, limit: 1 } } }),
  );
  return body.jobs?.[0]?.id;
}

/** HOSTS_NOT_COVERED_IN_ZONE is the code of the 409 that refuses a re-approval whose
 * replay would leave out hosts that joined a zone the first approval already started its
 * run in (internal/api coveredZonesCode, backlog 2.142): a contract, not prose. */
export const HOSTS_NOT_COVERED_IN_ZONE = 'hosts_not_covered_in_zone';

/** CoveredZone is one zone of that refusal with the hosts that joined it. */
export interface CoveredZone {
  id: string;
  name: string;
  hosts: { id: string; name: string }[];
}

/** coveredZonesOf reads the zones of that refusal from an approval's error; undefined for
 * any other error. */
export function coveredZonesOf(err: unknown): CoveredZone[] | undefined {
  if (!(err instanceof ApiError) || err.status !== 409) return undefined;
  const body = err.body as { code?: unknown; zones?: unknown } | undefined;
  if (body?.code !== HOSTS_NOT_COVERED_IN_ZONE || !Array.isArray(body.zones)) return undefined;
  return body.zones as CoveredZone[];
}

/** RUN_REQUEST_CHANGED is the 409 detail of an approval whose picture changed since the
 * page loaded it (internal/runrequests ErrChanged): a contract, not prose. */
export const RUN_REQUEST_CHANGED = 'changed since you looked at it; review it again';
