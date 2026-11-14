import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { CheckDefinitionDefInput } from '../types.ts';

const queryKey = ['admin', 'check-definitions'] as const;

/** The installation's own check definitions (instance administrators only;
 * GET /admin/checks/definitions admits no other caller) —
 * useSharedRepoDefinitions.ts's own doc comment, Task 7's own application:
 * every organization holds a projection of each one, this list is the
 * definitions themselves. */
/** Every organization holds a projection of each definition, and a
 * projection naming a secret counts in that organization's secret list
 * ("used by"), so a write refreshes the secret lists of every organization
 * as well. */
function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey });
    void qc.invalidateQueries({ queryKey: ['secrets'] });
  };
}

export function useCheckDefinitions() {
  return useQuery({ queryKey, queryFn: () => unwrap(api.GET('/api/v1/admin/checks/definitions')) });
}

export function useCreateCheckDefinition() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (body: CheckDefinitionDefInput) => unwrap(api.POST('/api/v1/admin/checks/definitions', { body })),
    onSuccess: invalidate,
  });
}

export function useUpdateCheckDefinition(id: string) {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (body: CheckDefinitionDefInput) =>
      unwrap(api.PUT('/api/v1/admin/checks/definitions/{id}', { params: { path: { id } }, body })),
    onSuccess: invalidate,
  });
}

/** useDeleteRepoDefinition's own doc comment, unchanged reasoning — except a
 * check definition's own delete NEVER refuses with an in-use conflict at
 * all (spec decision 5 has nothing to block on: nothing in the schema
 * references check_definitions, internal/shared's own doc comment on
 * DeleteCheckDefinitionWithProjections), so this mutation only ever needs
 * to surface a network or server failure, never a 409's own detail. */
export function useDeleteCheckDefinition() {
  const invalidate = useInvalidate();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/admin/checks/definitions/{id}', { params: { path: { id } } })),
    onSuccess: invalidate,
  });
}
