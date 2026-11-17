import { keepPreviousData, useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { ServerPageQuery } from './paging.ts';

export interface ActivityFilter {
  actor?: string;
  objectType?: string;
  since?: string;
}

/** Events fetched per page; the API defaults to 20 and clamps at 100
 * (internal/audit MaxListLimit). */
export const ACTIVITY_PAGE_SIZE = 50;

/**
 * useOrgActivity is the paginated activity feed, following the API's
 * `next_cursor` (an empty string on the last page) through
 * `fetchNextPage` — the same shape as useHosts.ts's useHosts, for the
 * same reason: a single cursor-less request silently ended the feed at
 * the server's default page size. The cursor is managed by the infinite
 * query itself (it is no longer part of the filter), and the filter is in
 * the query key, so changing it starts over from the first page.
 */
export function useOrgActivity(org: string, filter: ActivityFilter = {}) {
  return useInfiniteQuery({
    queryKey: ['activity', org, filter],
    initialPageParam: '',
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/activity', {
          params: {
            path: { org },
            query: {
              actor: filter.actor ?? '',
              object_type: filter.objectType ?? '',
              since: filter.since ?? '',
              cursor: pageParam || undefined,
              limit: ACTIVITY_PAGE_SIZE,
            },
          },
        }),
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    enabled: !!org,
  });
}

/**
 * useMyActivityPage is the signed-in person's own activity across every
 * organization, one numbered page per request (the API's page mode, which
 * also answers the total). The previous page stays on screen while the next
 * one loads. `enabled` waits for the list's preferences (useListView's
 * `ready`), so the first request already has the person's page size.
 */
export function useMyActivityPage(filter: Pick<ActivityFilter, 'objectType'>, page: ServerPageQuery, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['my-activity', filter, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/me/activity', {
          params: {
            query: {
              object_type: filter.objectType || undefined,
              page: page.page,
              per_page: page.per_page,
              sort: page.sort as 'at' | undefined,
              order: page.order,
            },
          },
        }),
      ),
    placeholderData: keepPreviousData,
    enabled: opts.enabled ?? true,
  });
}
