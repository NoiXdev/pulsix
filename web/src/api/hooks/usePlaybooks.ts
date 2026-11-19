import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['playbooks', org] });
    // Job templates reference playbooks; a deleted one shows up there.
    void qc.invalidateQueries({ queryKey: ['templates', org] });
  };
}

export function usePlaybooks(org: string) {
  return useQuery({
    queryKey: ['playbooks', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/playbooks', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export function usePlaybook(org: string, id: string) {
  return useQuery({
    queryKey: ['playbooks', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/playbooks/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
  });
}

/** gcTime: 0 — the inline document is the user's draft; nothing secret,
 * but there is no reason to keep a stale editor buffer cached. */
export function useCreatePlaybook(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: { inline: string }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/playbooks', { params: { path: { org } }, body })),
    gcTime: 0,
    onSuccess: invalidate,
  });
}

export function useUpdatePlaybook(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: { inline: string }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/playbooks/{id}', { params: { path: { org, id } }, body })),
    gcTime: 0,
    onSuccess: invalidate,
  });
}

/** The deleted playbook's own read is not refetched: the page showing it
 * would answer "not found" on its way out. The caller drops it from the
 * cache once that page is gone (`forget`). */
export function useDeletePlaybook(org: string) {
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/playbooks/{id}', { params: { path: { org, id } } })),
    onSuccess: (_data, id) => {
      void qc.invalidateQueries({ queryKey: ['playbooks', org], predicate: (q) => q.queryKey[2] !== id });
      void qc.invalidateQueries({ queryKey: ['templates', org] });
    },
  });
  const forget = (id: string) => qc.removeQueries({ queryKey: ['playbooks', org, id], exact: true });
  return { ...mutation, forget };
}
