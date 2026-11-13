import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/** useMonitoringSettings reads the organization's monitoring settings
 * (GET /orgs/{org}/settings/monitoring, org.settings.read — every member):
 * today the days without contact after which a client host's absent
 * warning opens (backlog 3.57; 0 = off). */
export function useMonitoringSettings(org: string, enabled = true) {
  return useQuery({
    queryKey: ['monitoring-settings', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/settings/monitoring', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

/** useUpdateMonitoringSettings replaces them (PUT, org.settings.write —
 * admins). The server takes 0..365 and answers 422 otherwise. Resolves once
 * the fresh value is in the cache, so a form that drops its draft on
 * success never shows the old one again. */
export function useUpdateMonitoringSettings(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (clientAbsentDays: number) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/settings/monitoring', { params: { path: { org } }, body: { client_absent_days: clientAbsentDays } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['monitoring-settings', org] }),
  });
}
