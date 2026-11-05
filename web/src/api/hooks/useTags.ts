import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { Host, Tag } from '../types.ts';
import { ifMatch } from './useHosts.ts';

export function useHostTags(org: string, hostId: string) {
  return useQuery({
    queryKey: ['host-tags', org, hostId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}/tags', { params: { path: { org, id: hostId } } })),
    enabled: !!org && !!hostId,
  });
}

/** useSetHostTags replaces the host's manual tags under If-Match: a stale
 * revision answers 412 (an ApiError with that status). The write bumps the
 * host's revision, so the new one goes straight into the cached host: the
 * next write from the same page must not send the old one while the refetch
 * is still in flight. */
export function useSetHostTags(org: string, hostId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ tags, revision }: { tags: Array<Pick<Tag, 'key' | 'value'>>; revision: number }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/hosts/{id}/tags', { params: { path: { org, id: hostId }, header: ifMatch(revision) }, body: tags })),
    onSuccess: (res) => {
      qc.setQueryData<{ host: Host }>(['host', org, hostId], (cached) =>
        cached ? { ...cached, host: { ...cached.host, revision: res.host_revision } } : cached,
      );
      void qc.invalidateQueries({ queryKey: ['host-tags', org, hostId] });
      void qc.invalidateQueries({ queryKey: ['hosts', org] });
      void qc.invalidateQueries({ queryKey: ['host', org, hostId] });
    },
  });
}

/** useHostQuery runs the tag-selector query box (design's HOST LIST filter
 * bar): a syntactically invalid expression surfaces through the mutation's
 * `error`, letting the query box show the parser's own message inline
 * instead of a generic failure. */
export function useHostQuery(org: string) {
  return useMutation({
    mutationFn: (selector: string) =>
      unwrap(api.POST('/api/v1/orgs/{org}/hosts/query', { params: { path: { org } }, body: { selector } })),
  });
}
