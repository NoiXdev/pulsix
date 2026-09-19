import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['workers', org] });
    void qc.invalidateQueries({ queryKey: ['worker-enrollments', org] });
  };
}

/** useWorkers lists the org's workers; refreshed every 30 s so online and
 * offline follow the heartbeat without a reload. */
export function useWorkers(org: string, enabled = true) {
  return useQuery({
    queryKey: ['workers', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/workers', { params: { path: { org } } })),
    enabled: !!org && enabled,
    refetchInterval: 30_000,
  });
}

export function useWorkerEnrollments(org: string, enabled = true) {
  return useQuery({
    queryKey: ['worker-enrollments', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/worker-enrollments', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

/** useCreateWorkerEnrollment returns the token once; gcTime 0 keeps it
 * out of the query cache like every other secret-bearing mutation. */
export function useCreateWorkerEnrollment(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: { zone_id: string; name: string }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/worker-enrollments', { params: { path: { org } }, body })),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

export function useWithdrawWorkerEnrollment(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/worker-enrollments/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useRevokeWorker(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/api/v1/orgs/{org}/workers/{id}/revoke', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useDeleteWorker(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/workers/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}
