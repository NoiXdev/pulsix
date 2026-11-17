import { useQuery, type QueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import { versionBaseline } from '../../lib/versionBaseline.ts';
import type { SetupCompleteInput } from '../types.ts';

export const setupStatusQueryKey = ['setup-status'] as const;
const setupStatusQueryFn = async () => {
  const status = await unwrap(api.GET('/api/v1/setup/status'));
  if (versionBaseline.value === null && status.version) versionBaseline.value = status.version;
  return status;
};

/**
 * The one set of options every reader of the setup status shares. The
 * request is public and the first thing a cold load asks for: on the default
 * network mode it would pause offline and the app would sit on "Loading…" for
 * good, so it runs regardless and fails like any other request. Because every
 * reader uses these options, no reader's fetch can be the paused one.
 */
const setupStatusQuery = {
  queryKey: setupStatusQueryKey,
  queryFn: setupStatusQueryFn,
  networkMode: 'always',
} as const;

/** Polled once on app load (src/routes root logic) to decide whether the
 * SPA redirects into /setup, /login, or straight into an organization. */
export function useSetupStatus() {
  return useQuery(setupStatusQuery);
}

export const versionWatchQueryKey = ['setup-status', 'version-watch'] as const;

/**
 * The version watcher's own reader of the same endpoint (backlog 3.95), on a
 * key of its own so the badge's query keeps its cache and timing. It polls
 * every five minutes and when the tab returns to the foreground; a failed
 * read leaves `data` at the last good answer.
 */
export function useVersionWatch() {
  return useQuery({
    queryKey: versionWatchQueryKey,
    queryFn: setupStatusQueryFn,
    networkMode: 'always',
    refetchInterval: 5 * 60_000,
    refetchOnWindowFocus: true,
    staleTime: 0,
  });
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
  return qc.query({ ...setupStatusQuery, staleTime: 0 });
}
