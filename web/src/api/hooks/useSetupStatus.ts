import { useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { SetupCompleteInput } from '../types.ts';

/** Polled once on app load (src/routes/root logic) to decide whether the
 * SPA redirects into /setup, /login, or straight into an organization. */
export function useSetupStatus() {
  return useQuery({
    queryKey: ['setup-status'],
    queryFn: () => unwrap(api.GET('/api/v1/setup/status')),
  });
}

export async function completeSetup(body: SetupCompleteInput) {
  return unwrap(api.POST('/api/v1/setup/complete', { body }));
}
