import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { RepoInput } from '../types.ts';

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['repos', org] });
    // A sync produces playbooks; an edit can change what a sync will do.
    void qc.invalidateQueries({ queryKey: ['playbooks', org] });
    void qc.invalidateQueries({ queryKey: ['jobs', org] });
  };
}

export function useRepos(org: string) {
  return useQuery({
    queryKey: ['repos', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/repos', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export function useRepo(org: string, id: string) {
  return useQuery({
    queryKey: ['repos', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/repos/{id}', { params: { path: { org, id } } })),
    enabled: !!org && !!id,
  });
}

export function useCreateRepo(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: RepoInput) => unwrap(api.POST('/api/v1/orgs/{org}/repos', { params: { path: { org } }, body })),
    onSuccess: invalidate,
  });
}

export function useUpdateRepo(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: RepoInput) => unwrap(api.PUT('/api/v1/orgs/{org}/repos/{id}', { params: { path: { org, id } }, body })),
    onSuccess: invalidate,
  });
}

export function useDeleteRepo(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/repos/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** useClearRepoKey removes a repository's git server key pin; the next
 * successful sync re-pins whatever the server then presents. */
export function useClearRepoKey(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: () => unwrap(api.DELETE('/api/v1/orgs/{org}/repos/{id}/host-key', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** useSyncRepo starts a repo sync (a sync_repo job) and returns its 202
 * body — { job } — so the page can point at the run view. */
export function useSyncRepo(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/api/v1/orgs/{org}/repos/{id}/sync', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}
