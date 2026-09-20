import { useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/** useCalendar loads the schedule occurrences and maintenance windows of
 * [from, to) (ISO 8601, at most six weeks apart). */
export function useCalendar(org: string, from: string, to: string) {
  return useQuery({
    queryKey: ['calendar', org, from, to],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/calendar', { params: { path: { org }, query: { from, to } } })),
    enabled: !!org,
  });
}
