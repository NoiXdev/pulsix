import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { BootstrapInput } from '../types.ts';

export function useHostTest(org: string, hostId: string) {
  return useQuery({
    // retry: false — a 404 ("no test yet") is a normal state of a host that
    // was never tested, not something to retry.
    retry: false,
    queryKey: ['hostops', org, 'test', hostId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}/connection-test', { params: { path: { org, id: hostId } } })),
    enabled: !!org && !!hostId,
  });
}

function useInvalidateHostOps(org: string, hostId?: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['hostops', org] });
    if (hostId) void qc.invalidateQueries({ queryKey: ['host', org, hostId] });
  };
}

export function useStartConnectionTest(org: string, hostId: string) {
  const invalidate = useInvalidateHostOps(org);
  return useMutation({
    mutationFn: () => unwrap(api.POST('/api/v1/orgs/{org}/hosts/{id}/connection-test', { params: { path: { org, id: hostId } } })),
    onSuccess: invalidate,
  });
}

export function useSetHostKey(org: string, hostId: string) {
  const invalidate = useInvalidateHostOps(org, hostId);
  return useMutation({
    mutationFn: (fingerprint: string) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/hosts/{id}/host-key', { params: { path: { org, id: hostId } }, body: { fingerprint } })),
    onSuccess: invalidate,
  });
}

export function useClearHostKey(org: string, hostId: string) {
  const invalidate = useInvalidateHostOps(org, hostId);
  return useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/orgs/{org}/hosts/{id}/host-key', { params: { path: { org, id: hostId } } })),
    onSuccess: invalidate,
  });
}

/** gcTime: 0 — the body carries the one-time login. It is never stored
 * server-side either (the payload is wiped at the job's terminal state);
 * not keeping it in the mutation cache is the client's half of the same
 * promise. */
export function useBootstrapHost(org: string, hostId: string) {
  const invalidate = useInvalidateHostOps(org, hostId);
  return useMutation({
    mutationFn: (body: BootstrapInput) =>
      unwrap(api.POST('/api/v1/orgs/{org}/hosts/{id}/bootstrap', { params: { path: { org, id: hostId } }, body })),
    gcTime: 0,
    onSuccess: invalidate,
  });
}
