import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/** The organization's secret store (backlog 3.13d). No response carries a
 * value; the only place a value exists in the SPA is the dialog's own
 * inputs and the PUT that sends them. */
export function useSecrets(org: string) {
  return useQuery({
    queryKey: ['secrets', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/secrets', { params: { path: { org } } })),
    enabled: !!org,
  });
}

/** A write refreshes the list and the host pages' checks, whose
 * missing-secret warning for a server-run check reads the store's names. */
function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['secrets', org] });
    void qc.invalidateQueries({ queryKey: ['host-checks', org] });
  };
}

export interface PutSecretInput {
  name: string;
  fields: Record<string, string>;
  /** Create only: the server answers 412 when the name is taken instead
   * of replacing that secret (If-None-Match: *). */
  createOnly: boolean;
}

/** usePutSecret creates or wholly replaces a secret. gcTime: 0 — the
 * variables carry the values; the dialog also calls reset() once the call
 * has settled, so the mutation cache never keeps them. */
export function usePutSecret(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ name, fields, createOnly }: PutSecretInput) =>
      unwrap(
        api.PUT('/api/v1/orgs/{org}/secrets/{name}', {
          params: { path: { org, name }, ...(createOnly ? { header: { 'If-None-Match': '*' } } : {}) },
          body: { fields },
        }),
      ),
    gcTime: 0,
    onSuccess: invalidate,
  });
}

/** useDeleteSecret refreshes the list whatever the answer: a 409 means its
 * "used by" was stale. */
export function useDeleteSecret(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (name: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/secrets/{name}', { params: { path: { org, name } } })),
    onSettled: invalidate,
  });
}
