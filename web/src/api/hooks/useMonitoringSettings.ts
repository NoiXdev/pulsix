import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/** The organization's retention choice in days; null = the instance default. */
export type RetentionChoice = 30 | 90 | 365 | null;

/** useMonitoringSettings reads the organization's monitoring settings
 * (GET /orgs/{org}/settings/monitoring, org.settings.read — every member):
 * today the days without contact after which a client host's absent
 * warning opens (backlog 3.57; 0 = off), and how long measurements are kept
 * (backlog 3.15: the choice, the instance maximum and the effective days). */
export function useMonitoringSettings(org: string, enabled = true) {
  return useQuery({
    queryKey: ['monitoring-settings', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/settings/monitoring', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

/** useUpdateMonitoringSettings writes them (PUT, org.settings.write —
 * admins). The server takes client_absent_days 0..365 and answers 422
 * otherwise. retentionDays is sent only when it is not undefined: left out,
 * the server keeps the stored choice (so a save that did not touch the
 * retention never overwrites another admin's newer choice); null resets it
 * to the instance default. Resolves once the fresh value is in the cache,
 * so a form that drops its draft on success never shows the old one again. */
export function useUpdateMonitoringSettings(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { clientAbsentDays: number; retentionDays?: RetentionChoice }) =>
      unwrap(
        api.PUT('/api/v1/orgs/{org}/settings/monitoring', {
          params: { path: { org } },
          body:
            input.retentionDays === undefined
              ? { client_absent_days: input.clientAbsentDays }
              : { client_absent_days: input.clientAbsentDays, retention_days: input.retentionDays },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['monitoring-settings', org] }),
  });
}
