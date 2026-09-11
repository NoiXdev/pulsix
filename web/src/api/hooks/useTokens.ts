import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

export function useTokens(org: string) {
  return useQuery({
    queryKey: ['tokens', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/tokens', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export function useCreateToken(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { name: string; role: 'owner' | 'admin' | 'operator' | 'viewer'; expires_at?: string }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/tokens', { params: { path: { org } }, body })),
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
