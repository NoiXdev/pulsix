import { useEffect, useState } from 'react';
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
 * useSettledMe is useMe for a screen that redirects on what /auth/me says
 * (RootRedirect, EnrolmentLayout, ChangePasswordPage). It fetches on every
 * mount, and `settled` turns true once an answer has landed since this
 * mount; it never turns back, so a later background refetch does not
 * take the screen away. Until then `data` may be a cached answer from
 * before the session changed (a password just replaced, a factor just
 * enrolled): acting on it bounced the session through a screen it no
 * longer belongs on, so these screens show loading until `settled`.
 *
 * The fetch runs whatever the browser says about the network
 * (`networkMode: 'always'`): offline it fails instead of pausing, which
 * left these screens on loading for good. `failed` is true while the
 * screen has an answer since this mount but no successful one — the fetch
 * on mount failed, so whatever `data` holds is the stale cached answer and
 * the screen shows the failure instead. Once a fetch since this mount has
 * succeeded it stays false: a later background refetch that fails is a
 * hiccup for useQueryGuard (a toast), not a reason to take the screen away.
 *
 * The other /auth/me readers (useMe: the i18n provider for the whole
 * session, the settings pages) keep the default network mode, so offline
 * their fetch pauses — and a fetch this screen starts while one is paused
 * only resumes that one, which still waits for the network. So until this
 * screen has its answer, a paused fetch is cancelled (its state reverted)
 * and fetched again with this screen's options. Its own fetch never
 * pauses, so a pause is always another reader's.
 */
export function useSettledMe(opts: { enabled?: boolean } = {}) {
  const me = useQuery({
    queryKey: meQueryKey,
    queryFn: meQueryFn,
    retry: false,
    enabled: opts.enabled,
    refetchOnMount: 'always',
    networkMode: 'always',
  });
  const settled = me.isFetchedAfterMount;
  const qc = useQueryClient();
  const { refetch } = me;
  const heldByPausedFetch = opts.enabled !== false && !settled && me.fetchStatus === 'paused';
  useEffect(() => {
    if (!heldByPausedFetch) return;
    void qc.cancelQueries({ queryKey: meQueryKey, fetchStatus: 'paused' }).then(() => refetch());
  }, [heldByPausedFetch, qc, refetch]);
  const [answered, setAnswered] = useState(false);
  if (!answered && settled && me.isSuccess) setAnswered(true);
  return { me, settled, failed: settled && me.isError && !answered };
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
 * `query` sidesteps the "is anything observing this key right now"
 * question entirely: it always performs the fetch and only resolves once
 * the cache holds the fresh result, so awaiting it before navigating
 * guarantees RootRedirect's next mount sees the real, current state.
 */
export async function refetchMe(qc: QueryClient) {
  return qc.query({ queryKey: meQueryKey, queryFn: meQueryFn });
}

export function useRefetchMe() {
  const qc = useQueryClient();
  return () => refetchMe(qc);
}
