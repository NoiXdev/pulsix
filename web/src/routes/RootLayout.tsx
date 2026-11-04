import { useEffect } from 'react';
import { Outlet, useRouter } from '@tanstack/react-router';
import { onPasswordChangeRequired } from '../api/client.ts';

/** Where a refusal for a pending password change does not take the person:
 * the change itself, and second-factor enrolment, which completes the
 * session before the person has saved their recovery codes and then
 * continues to the root redirect on its own. */
const STAY = new Set(['/account/password', '/account/enrol']);

/**
 * RootLayout wraps every page. It takes a session whose account must
 * replace a password an administrator set to /account/password the moment
 * any request is refused with password_change_required (backlog 7.74) —
 * wherever the person is, whatever page or hook sent the request — so no
 * page has to recognize that refusal itself.
 */
export function RootLayout() {
  const router = useRouter();

  useEffect(
    () =>
      onPasswordChangeRequired(() => {
        if (STAY.has(router.state.location.pathname)) return;
        void router.navigate({ to: '/account/password' });
      }),
    [router],
  );

  return <Outlet />;
}
