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
