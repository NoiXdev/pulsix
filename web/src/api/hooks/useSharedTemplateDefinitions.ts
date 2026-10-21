import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { TemplateDefinitionInput } from '../types.ts';

const queryKey = ['admin', 'template-definitions'] as const;

/** The installation's own job template definitions (instance administrators
 * only; GET /admin/templates/definitions admits no other caller) —
 * useSharedRepoDefinitions.ts's own doc comment, Task 6's own application:
 * every organization holds a projection of each one, this list is the
 * definitions themselves. */
export function useTemplateDefinitions() {
  return useQuery({ queryKey, queryFn: () => unwrap(api.GET('/api/v1/admin/templates/definitions')) });
}

export function useCreateTemplateDefinition() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: TemplateDefinitionInput) => unwrap(api.POST('/api/v1/admin/templates/definitions', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  });
}

export function useUpdateTemplateDefinition(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: TemplateDefinitionInput) =>
      unwrap(api.PUT('/api/v1/admin/templates/definitions/{id}', { params: { path: { id } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  });
}

/** useDeleteRepoDefinition's own doc comment, unchanged reasoning: a
 * *NameCollisionError can never reach a delete, so ErrInUse is the only
 * failure this needs to surface. */
export function useDeleteTemplateDefinition() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/admin/templates/definitions/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  });
}
