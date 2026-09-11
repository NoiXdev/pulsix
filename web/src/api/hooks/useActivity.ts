import { useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

export interface ActivityFilter {
  actor?: string;
  objectType?: string;
  since?: string;
  cursor?: string;
}

export function useOrgActivity(org: string, filter: ActivityFilter = {}) {
  return useQuery({
    queryKey: ['activity', org, filter],
    queryFn: () =>
      unwrap(
        api.GET('/api/v1/orgs/{org}/activity', {
          params: {
            path: { org },
            query: {
              actor: filter.actor ?? '',
              object_type: filter.objectType ?? '',
              since: filter.since ?? '',
              cursor: filter.cursor ?? '',
            },
          },
        }),
      ),
    enabled: !!org,
  });
}

