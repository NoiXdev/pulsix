import { useQuery, type QueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { SetupCompleteInput } from '../types.ts';

export const setupStatusQueryKey = ['setup-status'] as const;
const setupStatusQueryFn = () => unwrap(api.GET('/api/v1/setup/status'));

/** Polled once on app load (src/routes root logic) to decide whether the
 * SPA redirects into /setup, /login, or straight into an organization. */
export function useSetupStatus() {
  return useQuery({ queryKey: setupStatusQueryKey, queryFn: setupStatusQueryFn });
}

export async function completeSetup(body: SetupCompleteInput) {
  return unwrap(api.POST('/api/v1/setup/complete', { body }));
}

/**
 * refetchSetupStatus is useMe.ts's refetchMe, for the same finding
 * ("Important 3") but the setup-status query: after POST /setup/complete
 * succeeds, the cached `completed: false` is stale the same way a stale
 * `mfa_complete` is — the next time RootRedirect mounts (a hard reload,
 * or navigating back to / later) it would otherwise send a fully set up
 * instance back to /setup. Called with an await before the wizard
 * navigates away, so the cache is already current by the time anything
 * reads it again.
 */
export async function refetchSetupStatus(qc: QueryClient) {
  return qc.fetchQuery({ queryKey: setupStatusQueryKey, queryFn: setupStatusQueryFn });
}
