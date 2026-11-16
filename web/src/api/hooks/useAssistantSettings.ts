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

/** useUpdateAssistantSettings turns it on or off (PUT, org.settings.write —
 * admins, in a browser session only, so a token can never turn it on). The
 * answer goes straight into the cache, so the switch shows what was stored. */
export function useUpdateAssistantSettings(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (enabled: boolean) =>
      unwrap(
        api.PUT('/api/v1/orgs/{org}/settings/assistant', {
          params: { path: { org } },
          body: { assistant_enabled: enabled },
        }),
      ),
    onSuccess: (data) => qc.setQueryData(['assistant-settings', org], data),
  });
}
