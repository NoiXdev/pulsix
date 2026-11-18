import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { ServerPageQuery } from './paging.ts';

export interface ActivityFilter {
  actor?: string;
  objectType?: string;
  since?: string;
}

/**
 * useOrgActivityPage is the organization's activity feed, one numbered page
 * per request (the API's page mode, which also answers the total). The
 * previous page stays on screen while the next one loads. `enabled` waits
 * for the list's preferences (useListView's `ready`), so the first request
 * already has the person's page size.
 */
export function useOrgActivityPage(org: string, filter: ActivityFilter, page: ServerPageQuery, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['activity', org, 'page', filter, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/activity', {
          params: {
            path: { org },
            query: {
              actor: filter.actor || undefined,
              object_type: filter.objectType || undefined,
              since: filter.since || undefined,
              page: page.page,
              per_page: page.per_page,
              sort: page.sort as 'at' | undefined,
              order: page.order,
            },
          },
        }),
      ),
    placeholderData: keepPreviousData,
    enabled: !!org && (opts.enabled ?? true),
  });
}

/**
 * useMyActivityPage is the signed-in person's own activity across every
 * organization, one numbered page per request (the API's page mode, which
 * also answers the total). The previous page stays on screen while the next
 * one loads. `enabled` waits for the list's preferences (useListView's
 * `ready`), so the first request already has the person's page size.
 */
export function useMyActivityPage(filter: Pick<ActivityFilter, 'objectType' | 'since'>, page: ServerPageQuery, opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['my-activity', filter, page],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/me/activity', {
          params: {
            query: {
              object_type: filter.objectType || undefined,
              since: filter.since || undefined,
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
