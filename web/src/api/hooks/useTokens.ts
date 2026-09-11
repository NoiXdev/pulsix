import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

export function useTokens(org: string) {
  return useQuery({
    queryKey: ['tokens', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/tokens', { params: { path: { org } } })),
    enabled: !!org,
  });
}

/**
 * gcTime: 0 is the Critical fix (task-15a-report.md fix round 1): `data`
 * here carries the new token's plaintext, which the API never persists
 * and never returns again — TanStack Query's mutation cache would
 * otherwise keep it reachable via `queryClient.getMutationCache().getAll()`
 * for five minutes after the create dialog closes. TokensPage.tsx also
 * calls `.reset()` once the plaintext has been copied/dismissed.
 */
export function useCreateToken(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { name: string; role: 'owner' | 'admin' | 'operator' | 'viewer'; expires_at?: string }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/tokens', { params: { path: { org } }, body })),
    gcTime: 0,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['tokens', org] }),
  });
}

export function useRevokeToken(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/tokens/{id}', { params: { path: { org, id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['tokens', org] }),
  });
}
