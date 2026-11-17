import { useCallback, useEffect, useMemo, useState } from 'react';
import { Outlet, useRouter, useRouterState } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { onPasswordChangeRequired, onSessionLocked } from '../api/client.ts';
import { refetchMe, useMe } from '../api/hooks/useMe.ts';
import { LockScreen } from '../features/lock/LockScreen.tsx';
import { onSessionMessage } from '../features/lock/lockChannel.ts';
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

  useEffect(() => onSessionLocked(() => setLockedHere(true)), []);

  useEffect(
    () =>
      onSessionMessage((msg) => {
        if (msg.type === 'locked') {
          setLockedHere(true);
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
    [qc],
  );

  // The page is gone from the tree by now (this runs after the commit that
  // swapped it for the lock screen); drop what it had fetched.
  useEffect(() => {
    if (!locked) return;
    qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'me' });
  }, [locked, qc]);

  const value = useMemo(() => ({ locked, setLocked }), [locked, setLocked]);

  // Until /auth/me has answered, a page that needs a session is not drawn:
  // it may be one a lock must not show.
  const waiting = !publicPage && me.isPending && !lockedHere;

  return (
    <LockStateContext.Provider value={value}>
      {locked ? <LockScreen /> : waiting ? null : <Outlet />}
    </LockStateContext.Provider>
  );
}
