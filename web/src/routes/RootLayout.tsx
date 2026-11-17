import { useCallback, useEffect, useMemo, useState } from 'react';
import { Outlet, useRouter, useRouterState } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { onPasswordChangeRequired, onSessionLocked } from '../api/client.ts';
import { refetchMe, useMe } from '../api/hooks/useMe.ts';
import { ConfirmProvider } from '../ui/confirm.tsx';
import { QueryLoading } from '../ui/QueryState.tsx';
import { LockScreen } from '../features/lock/LockScreen.tsx';
import { onSessionMessage } from '../features/lock/lockChannel.ts';
import { VersionWatcher } from '../features/version/VersionWatcher.tsx';
import { ExpiryWatcher } from '../features/session/ExpiryWatcher.tsx';
import { LockStateContext } from '../features/lock/lockState.ts';

/** Where a refusal for a pending password change does not take the person:
 * the change itself, and second-factor enrolment, which completes the
 * session before the person has saved their recovery codes and then
 * continues to the root redirect on its own. */
const STAY = new Set(['/account/password', '/account/enrol']);

/** Pages a person sees without a session. They never ask /auth/me, and a
 * lock never covers them. */
const PUBLIC = ['/login', '/setup', '/forgot-password', '/reset-password', '/invite'];

function isPublic(pathname: string): boolean {
  return PUBLIC.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

/**
 * RootLayout wraps every page. It takes a session whose account must
 * replace a password an administrator set to /account/password the moment
 * any request is refused with password_change_required (backlog 7.74) —
 * wherever the person is, whatever page or hook sent the request — so no
 * page has to recognize that refusal itself.
 *
 * It also owns the session lock (backlog 3.92). While the session is
 * locked the lock screen is rendered *instead of* the routed page, so the
 * page unmounts and its data leaves the DOM, and the query cache is
 * emptied of everything but ['me']. Three things lock it: /auth/me saying
 * `locked` on load, any request refused with session_locked (that is how a
 * second tab of the same browser, sharing the cookie, follows), and the
 * BroadcastChannel message that makes the other tabs follow at once.
 */
export function RootLayout() {
  const router = useRouter();
  const qc = useQueryClient();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const publicPage = isPublic(pathname);
  const me = useMe({ enabled: !publicPage });
  const [lockedHere, setLockedHere] = useState(false);

  // Derived during render (React's reset-state-on-change pattern): a public
  // page starts from an unlocked state.
  if (publicPage && lockedHere) setLockedHere(false);

  const locked = !publicPage && (lockedHere || me.data?.locked === true);

  useEffect(
    () =>
      onPasswordChangeRequired(() => {
        if (STAY.has(router.state.location.pathname)) return;
        void router.navigate({ to: '/account/password' });
      }),
    [router],
  );

  const setLocked = useCallback((next: boolean) => setLockedHere(next), []);

  // A lock signal means nothing on a page that has no session (a tab left
  // on /login while another tab locked): signing in there starts a fresh,
  // unlocked session.
  const lockHere = useCallback(() => {
    if (!isPublic(router.state.location.pathname)) setLockedHere(true);
  }, [router]);

  useEffect(() => onSessionLocked(lockHere), [lockHere]);


  useEffect(
    () =>
      onSessionMessage((msg) => {
        if (msg.type === 'locked') {
          lockHere();
        } else if (msg.type === 'unlocked') {
          // Make /auth/me say unlocked before the page comes back.
          void refetchMe(qc)
            .catch(() => undefined)
            .then(() => {
              setLockedHere(false);
              void qc.invalidateQueries();
            });
        }
      }),
    [qc, lockHere],
  );

  // The page is gone from the tree by now (this runs after the commit that
  // swapped it for the lock screen); drop what it had fetched.
  useEffect(() => {
    if (!locked) return;
    qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'me' });
  }, [locked, qc]);

  const value = useMemo(() => ({ locked, setLocked }), [locked, setLocked]);

  // Until /auth/me has answered, a page that needs a session is not drawn:
  // it may be one a lock must not show. Only while the fetch is actually
  // running: offline it pauses and a failed one is over, and then the page
  // handles it (useSettledMe's recovery, the page's own error state) — if
  // /auth/me cannot be fetched its data cannot either, and a 403
  // session_locked still locks.
  const waiting = !publicPage && me.isPending && me.fetchStatus === 'fetching' && !lockedHere;

  return (
    <LockStateContext.Provider value={value}>
      {locked ? (
        <LockScreen />
      ) : waiting ? (
        <QueryLoading />
      ) : (
        // The confirm dialog lives here, not above the router, so a lock
        // unmounts it (its question resolves false) with the page it came from.
        <ConfirmProvider>
          <Outlet />
          {/* A session that must change its password cannot renew: the renew
              routes refuse it, so the dialog could only fail. */}
          {!publicPage && me.data?.mfa_complete && !me.data.must_change_password && <ExpiryWatcher />}
          {!publicPage && me.data?.mfa_complete && <VersionWatcher />}
        </ConfirmProvider>
      )}
    </LockStateContext.Provider>
  );
}
