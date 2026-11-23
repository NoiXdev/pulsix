import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import { ifMatch } from '../ifMatch.ts';
import type { PlaybookDryRunInput } from '../types.ts';

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

/** useUpdatePlaybook saves an editor playbook's text. With `revision` (the
 * one the draft's base came from) it sends If-Match, and a stale one
 * answers 412 (an ApiError with that status); without one (a manager that
 * keeps no revision) it saves unconditionally. */
export function useUpdatePlaybook(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ inline, revision }: { inline: string; revision?: number }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/playbooks/{id}', { params: { path: { org, id }, header: revision ? ifMatch(revision) : undefined }, body: { inline } })),
    gcTime: 0,
    onSuccess: invalidate,
  });
}

/** usePlaybookContent reads a repository playbook's text as of the last
 * sync, or why there is none. 404 (the repository was never synced) is a
 * state the page shows, not an error to retry. */
export function usePlaybookContent(org: string, id: string, enabled: boolean) {
  return useQuery({
    queryKey: ['playbooks', org, id, 'content'],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/playbooks/{id}/content', { params: { path: { org, id } } })),
    enabled: enabled && !!org && !!id,
    retry: false,
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

/** usePlaybookOutline reads the outline of a playbook text (the editor's
 * draft). Its own key prefix, so saving a playbook does not refetch it; a
 * short gcTime because the key holds the draft. The previous outline stays
 * while the next one loads; an invalid text answers 422, which is not
 * retried. */
export function usePlaybookOutlineQuery(org: string, content: string, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['playbook-outline', org, content],
    queryFn: ({ signal }) => unwrap(api.POST('/api/v1/orgs/{org}/playbooks/outline', { params: { path: { org } }, body: { content }, signal })),
    enabled: !!org && (opts.enabled ?? true),
    placeholderData: keepPreviousData,
    retry: false,
    gcTime: 30_000,
    staleTime: Infinity,
  });
}

/** useDryRunPlaybook starts a dry run of the playbook through one of its
 * templates (202, { jobs }), like a template run; the runs lists refetch. */
export function useDryRunPlaybook(org: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: PlaybookDryRunInput) => unwrap(api.POST('/api/v1/orgs/{org}/playbooks/{id}/dry-run', { params: { path: { org, id } }, body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['jobs', org] }),
  });
}
