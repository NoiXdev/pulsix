import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { components } from '../schema.d.ts';

/** What POST /orgs/{org}/tokens takes: `kind` defaults to api; `areas` is
 * required (non-empty) for an assistant token and refused for an API token. */
export type CreateTokenInput = Omit<components['schemas']['OrgTokenCreateInputBody'], '$schema'>;

/** What PATCH /orgs/{org}/tokens/{id} takes: every field is optional and only
 * the ones present change. `areas` is the full new list (assistant tokens
 * only); `never_expires` removes the expiry. */
export type UpdateTokenInput = Omit<components['schemas']['OrgTokenUpdateInputBody'], '$schema'>;

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
    mutationFn: (body: CreateTokenInput) =>
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

export function useUpdateToken(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: UpdateTokenInput }) =>
      unwrap(api.PATCH('/api/v1/orgs/{org}/tokens/{id}', { params: { path: { org, id } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['tokens', org] }),
  });
}
