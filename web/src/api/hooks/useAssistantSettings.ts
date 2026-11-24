import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/** useAssistantSettings reads whether assistant tokens may work in the
 * organization (GET /orgs/{org}/settings/assistant, org.settings.read —
 * every member). Absent a stored choice the server answers off. */
export function useAssistantSettings(org: string) {
  return useQuery({
    queryKey: ['assistant-settings', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/settings/assistant', { params: { path: { org } } })),
    enabled: !!org,
  });
}

/** useUpdateAssistantSettings changes the access switch or whether approved
 * runs start follow-ups, each on its own: a field left out keeps its stored
 * value server-side (PUT, org.settings.write —
 * admins, in a browser session only, so a token can never turn it on). The
 * answer goes straight into the cache, so the switch shows what was stored. */
export function useUpdateAssistantSettings(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { assistant_enabled?: boolean; approved_runs_start_follow_ups?: boolean }) =>
      unwrap(
        api.PUT('/api/v1/orgs/{org}/settings/assistant', {
          params: { path: { org } },
          body,
        }),
      ),
    onSuccess: (data) => qc.setQueryData(['assistant-settings', org], data),
  });
}
