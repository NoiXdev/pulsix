import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { TriggerInput } from '../types.ts';

/** TriggerTarget is what a trigger starts: a template run or a repo sync. */
export interface TriggerTarget {
  kind: 'template_run' | 'repo_sync';
  id: string;
  name: string;
}

const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['triggers', org] });
    void qc.invalidateQueries({ queryKey: ['trigger-deliveries', org] });
    void qc.invalidateQueries({ queryKey: ['trigger-pending', org] });
    void qc.invalidateQueries({ queryKey: ['jobs', org] });
  };
}

export function useTriggers(org: string, target: TriggerTarget) {
  const query = target.kind === 'template_run' ? { template_id: target.id } : { repo_id: target.id };
  return useQuery({
    queryKey: ['triggers', org, target.kind, target.id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/triggers', { params: { path: { org }, query } })),
    enabled: !!org && !!target.id,
  });
}

export function useTriggerDeliveries(org: string, id: string) {
  return useQuery({
    queryKey: ['trigger-deliveries', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/triggers/{id}/deliveries', { params: { path: { org, id }, query: { limit: 50 } } })),
    enabled: !!org && !!id,
  });
}

/** Secret-bearing mutations keep nothing in the cache (gcTime 0). */
export function useCreateTrigger(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: TriggerInput) => unwrap(api.POST('/api/v1/orgs/{org}/triggers', { params: { path: { org } }, body })),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

export function useUpdateTrigger(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ revision, ...body }: TriggerInput & { revision: number }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/triggers/{id}', { params: { path: { org, id }, header: ifMatch(revision) }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteTrigger(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/triggers/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useRotateTriggerSecret(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/api/v1/orgs/{org}/triggers/{id}/rotate-secret', { params: { path: { org, id } } })),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

export function useTestTrigger(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ id, payload }: { id: string; payload?: Record<string, unknown> }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/triggers/{id}/test', { params: { path: { org, id } }, body: payload ? { payload } : {} })),
    onSuccess: invalidate,
  });
}

/** PENDING_POLL_MS is how often a waiting collected run is read again, so
 * the card notices when it starts; SETTLING_POLL_MS how often a settling
 * trigger is asked whether one started waiting. */
const PENDING_POLL_MS = 5000;
const SETTLING_POLL_MS = 10000;

/** pendingPollInterval is how often the pending run is read: quickly while
 * one waits, slowly while the trigger has a settle delay, never otherwise. */
export function pendingPollInterval(settling: boolean, waiting: boolean): number | false {
  if (waiting) return PENDING_POLL_MS;
  return settling ? SETTLING_POLL_MS : false;
}

/**
 * useTriggerPending reads a trigger's collected run while it waits
 * (backlog 2.137): every few seconds while one waits and, for a trigger
 * with a settle delay (settling), every ten seconds otherwise, so a run that
 * starts waiting while the page is open shows. When the run it showed
 * changes (one started waiting, or it started, was discarded or dropped)
 * the delivery log is read again, so its deliveries say what became of them.
 */
export function useTriggerPending(org: string, id: string, enabled: boolean, settling = false) {
  const qc = useQueryClient();
  const key = ['trigger-pending', org, id];
  return useQuery({
    queryKey: key,
    queryFn: async () => {
      const before = qc.getQueryData<{ pending?: { id: string } }>(key);
      const res = await unwrap(api.GET('/api/v1/orgs/{org}/triggers/{id}/pending', { params: { path: { org, id } } }));
      if (before && before.pending?.id !== res.pending?.id) void qc.invalidateQueries({ queryKey: ['trigger-deliveries', org, id] });
      return res;
    },
    enabled: enabled && !!org && !!id,
    refetchInterval: (q) => pendingPollInterval(settling, !!q.state.data?.pending),
  });
}

export function useRunPendingTrigger(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/api/v1/orgs/{org}/triggers/{id}/pending/run', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useDiscardPendingTrigger(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/triggers/{id}/pending', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}
