import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { components } from '../schema.d.ts';

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

/** usePendingRunRequestCount is the number the navigation shows: the
 * pending requests as the server counts them (not the length of a list it
 * caps), polled so a request an assistant makes shows up without a reload.
 * Under the run-requests key, so a decision refreshes it with the lists. */
export function usePendingRunRequestCount(org: string, opts: { enabled?: boolean } = {}) {
  const query = useQuery({
    queryKey: ['run-requests', org, 'pending-count'],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/run-requests/pending-count', { params: { path: { org } } })),
    enabled: !!org && (opts.enabled ?? true),
    refetchInterval: PENDING_POLL_MS,
  });
  return query.data?.count;
}

/** useRunRequest is one request with its run picture (what the run would use
 * if it were approved now) and the fingerprint over that picture, which an
 * approval sends back. */
export function useRunRequest(org: string, id: string) {
  return useQuery({
    queryKey: ['run-request', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/run-requests/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
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
 * out, the server applies its default (all startable, or none). */
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

/** RUN_REQUEST_CHANGED is the 409 detail of an approval whose picture changed since the
 * page loaded it (internal/runrequests ErrChanged): a contract, not prose. */
export const RUN_REQUEST_CHANGED = 'changed since you looked at it; review it again';
