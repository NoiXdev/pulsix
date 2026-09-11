import { useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

export function useBastions(org: string) {
  return useQuery({
    queryKey: ['ssh', org, 'bastions'],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/bastions', { params: { path: { org } } })),
    enabled: !!org,
  });
}
