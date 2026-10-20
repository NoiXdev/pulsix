import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { RepoDefinitionInput } from '../types.ts';

const queryKey = ['admin', 'repo-definitions'] as const;

/** The installation's own repository definitions (instance administrators
 * only; GET /admin/repos/definitions admits no other caller). Every
 * organization holds a projection of each one — this list is the
 * definitions themselves, not any organization's own repos. */
export function useRepoDefinitions() {
  return useQuery({ queryKey, queryFn: () => unwrap(api.GET('/api/v1/admin/repos/definitions')) });
}

export function useCreateRepoDefinition() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: RepoDefinitionInput) => unwrap(api.POST('/api/v1/admin/repos/definitions', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  });
}

export function useUpdateRepoDefinition(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: RepoDefinitionInput) =>
      unwrap(api.PUT('/api/v1/admin/repos/definitions/{id}', { params: { path: { id } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  });
}

/** Refused with ErrInUse (naming the organization and what still
 * references the projection) or a *NameCollisionError can never reach a
 * delete — only a create or rename collides on a name — so the only
 * failure a delete needs to surface here is "in use". */
export function useDeleteRepoDefinition() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/admin/repos/definitions/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  });
}
