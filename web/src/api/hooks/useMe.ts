import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

export const meQueryKey = ['me'] as const;
const meQueryFn = () => unwrap(api.GET('/api/v1/auth/me'));

/** The signed-in principal (src/routes root logic reads `mfa_complete` to
 * decide whether a session still needs its second factor). Disabled
 * callers should pass `enabled: false` rather than relying on a 401.
 * Sign-in and sign-out do not invalidate this query on its own: they
 * reset the whole cache (lib/session.ts), and sign-in then re-fetches it
 * with refetchMe below. */
export function useMe(opts: { enabled?: boolean } = {}) {
  return useQuery({ queryKey: meQueryKey, queryFn: meQueryFn, retry: false, enabled: opts.enabled });
}

/**
 * refetchMe forces a fresh /auth/me fetch and waits for it, leaving the
 * shared query cache updated before the caller navigates — finding
 * "Important 3" (task-15a-report.md fix round 1): a pending session
 * reaches /, RootRedirect's useMe caches `mfa_complete: false`, and the
 * user is sent to the TOTP prompt. After a correct code, the old code
 * only called invalidateQueries — which merely marks the cache stale;
 * with no `useMe` observer mounted at that moment (the user is on
 * /login, not /), nothing actually refetches. Navigating back to /
 * remounts RootRedirect, which — on mount, with stale cached data
 * present — renders that stale `mfa_complete: false` synchronously
 * before its own background refetch resolves, bouncing a correctly
 * verified session straight back to the TOTP prompt.
 *
 * `fetchQuery` sidesteps the "is anything observing this key right now"
 * question entirely: it always performs the fetch and only resolves once
 * the cache holds the fresh result, so awaiting it before navigating
 * guarantees RootRedirect's next mount sees the real, current state.
 */
export async function refetchMe(qc: QueryClient) {
  return qc.fetchQuery({ queryKey: meQueryKey, queryFn: meQueryFn });
}

export function useRefetchMe() {
  const qc = useQueryClient();
  return () => refetchMe(qc);
}
