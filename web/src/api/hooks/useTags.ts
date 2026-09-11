import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { Tag } from '../types.ts';

export function useHostTags(org: string, hostId: string) {
  return useQuery({
    queryKey: ['host-tags', org, hostId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}/tags', { params: { path: { org, id: hostId } } })),
    enabled: !!org && !!hostId,
  });
}

export function useSetHostTags(org: string, hostId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (tags: Array<Pick<Tag, 'key' | 'value'>>) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/hosts/{id}/tags', { params: { path: { org, id: hostId } }, body: tags })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['host-tags', org, hostId] });
      void qc.invalidateQueries({ queryKey: ['hosts', org] });
    },
  });
}

export function useOrgTagKeys(org: string) {
  return useQuery({
    queryKey: ['tag-keys', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/tags', { params: { path: { org } } })),
    enabled: !!org,
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
