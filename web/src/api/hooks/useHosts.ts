import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { HostInput } from '../types.ts';

export interface HostListFilter {
  zone?: string;
  managed?: boolean;
  monitored?: boolean;
  q?: string;
}

/** Hosts fetched per page. The API defaults to 20 and clamps at 100
 * (internal/hosts MaxListLimit); asking explicitly keeps the page size a
 * decision made here rather than an accident of the server default. */
export const HOSTS_PAGE_SIZE = 50;

/**
 * useHosts is the paginated hosts list: one page per request, following
 * the API's `next_cursor` (an empty string on the last page) through
 * `fetchNextPage`. It used to issue a single request with no cursor, so
 * the list silently ended at the server's default page size and every
 * host beyond it was reachable only through search.
 *
 * The filter is part of the query key, so changing it starts over from
 * the first page. The key keeps the ['hosts', org] prefix every host
 * mutation below invalidates, which refetches all loaded pages.
 *
 * `keepPrevious` keeps the last filter's rows on screen while the next
 * filter's first page loads, for a picker whose list would otherwise
 * empty on every keystroke of its search.
 */
export function useHosts(org: string, filter: HostListFilter = {}, opts: { keepPrevious?: boolean } = {}) {
  return useInfiniteQuery({
    queryKey: ['hosts', org, filter],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/hosts', {
          params: {
            path: { org },
            query: {
              zone: filter.zone ?? '',
              managed: filter.managed === undefined ? '' : String(filter.managed),
              monitored: filter.monitored === undefined ? '' : String(filter.monitored),
              q: filter.q ?? '',
              cursor: pageParam || undefined,
              limit: HOSTS_PAGE_SIZE,
            },
          },
        }),
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    enabled: !!org,
    placeholderData: opts.keepPrevious ? keepPreviousData : undefined,
  });
}

function hostQueryOptions(org: string, id: string) {
  return {
    queryKey: ['host', org, id],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/hosts/{id}', { params: { path: { org, id } } })),
  };
}

export function useHost(org: string, id: string) {
  return useQuery({ ...hostQueryOptions(org, id), enabled: !!org && !!id });
}

export const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

/** A host write also moves what the detail page and the SSH card show: the
 * manual tags, the host's SSH settings (its jump host) and the inventory
 * preview whose connection resolves that jump host. */
function useInvalidateHost(org: string) {
  const qc = useQueryClient();
  return (id?: string) => {
    void qc.invalidateQueries({ queryKey: ['hosts', org] });
    void qc.invalidateQueries({ queryKey: id ? ['host', org, id] : ['host', org] });
    void qc.invalidateQueries({ queryKey: ['host-tags', org] });
    void qc.invalidateQueries({ queryKey: ['ssh', org] });
    void qc.invalidateQueries({ queryKey: ['inventory', org] });
  };
}

/** useCreateHost creates a host; `jump` and `tags` ride along, so the host
 * is never visible half-configured. */
export function useCreateHost(org: string) {
  const invalidate = useInvalidateHost(org);
  return useMutation({
    mutationFn: (body: HostInput) => unwrap(api.POST('/api/v1/orgs/{org}/hosts', { params: { path: { org } }, body })),
    onSuccess: () => invalidate(),
  });
}

/** useUpdateHost replaces a host's own fields under If-Match: a stale
 * revision answers 412 (an ApiError with that status). `jump` and `tags`
 * left out of the body stay as they are. */
export function useUpdateHost(org: string, id: string) {
  const qc = useQueryClient();
  const invalidate = useInvalidateHost(org);
  return useMutation({
    mutationFn: ({ revision, ...body }: HostInput & { revision: number }) =>
      unwrap(api.PATCH('/api/v1/orgs/{org}/hosts/{id}', { params: { path: { org, id }, header: ifMatch(revision) }, body })),
    onSuccess: (res, body) => {
      // The write bumped the revision: cache the response's host so a
      // following tag or SSH write sends the current one while the refetch
      // is still in flight. Not when the save also wrote tags or the jump
      // host: the response carries neither, so the tag and SSH caches are
      // stale until their refetch lands, and a sidebar write built from them
      // must answer 412 rather than pass If-Match and undo this save.
      if (body.tags === undefined && body.jump === undefined) {
        qc.setQueryData(['host', org, id], res);
      }
      invalidate(id);
    },
  });
}

/** useReloadHost refetches everything the host dialog pre-fills from — the
 * host (its revision), its tags, its SSH settings and the resolved
 * connection — and resolves with the fresh host. */
export function useReloadHost(org: string, id: string) {
  const qc = useQueryClient();
  return async () => {
    const res = await qc.fetchQuery({ ...hostQueryOptions(org, id), staleTime: 0 });
    await Promise.all([
      qc.refetchQueries({ queryKey: ['host-tags', org, id] }),
      qc.refetchQueries({ queryKey: ['ssh', org, 'host', id] }),
      qc.refetchQueries({ queryKey: ['inventory', org, 'host-preview', id] }),
    ]);
    return res.host;
  };
}

export function useSetHostCapabilities(org: string, id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: { managed?: boolean; monitored?: boolean }) =>
      unwrap(api.PATCH('/api/v1/orgs/{org}/hosts/{id}/capabilities', { params: { path: { org, id } }, body: patch })),
    onSuccess: (res) => {
      qc.setQueryData(['host', org, id], res);
      void qc.invalidateQueries({ queryKey: ['hosts', org] });
      void qc.invalidateQueries({ queryKey: ['host', org, id] });
    },
  });
}

export function useDeleteHost(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/hosts/{id}', { params: { path: { org, id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['hosts', org] }),
  });
}

export function useZones(org: string) {
  return useQuery({
    queryKey: ['zones', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/zones', { params: { path: { org } } })),
    enabled: !!org,
  });
}

export function useCreateZone(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { slug: string; name: string }) =>
      unwrap(api.POST('/api/v1/orgs/{org}/zones', { params: { path: { org } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['zones', org] }),
  });
}
