import { useEffect } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useSetupStatus } from '../api/hooks/useSetupStatus.ts';
import { useSettledMe } from '../api/hooks/useMe.ts';
import { useOrgs } from '../api/hooks/useOrgs.ts';
import { QueryError, QueryLoading } from '../ui/QueryState.tsx';
import { useT } from '../i18n/useT.ts';
import { errorStatus } from '../lib/apiError.ts';

/**
 * RootRedirect resolves the four startup states the task brief names,
 * strictly in this order:
 *
 *   1. setup not completed          -> /setup
 *   2. no session                   -> /login
 *   3. session, factor to verify    -> /login?step=totp
 *   4. session, no factor yet       -> /account/enrol (a second factor is
 *                                      mandatory; nothing else is reachable)
 *   5. session, temporary password  -> /account/password (the account must
 *                                      replace a password an administrator
 *                                      set; nothing else is reachable)
 *   6. ready session                -> /o/{first org}/hosts
 *
 * It calls useSetupStatus and useSettledMe directly (not a router loader)
 * so the same TanStack Query cache both hooks share is what every other
 * page reads from too — no separate fetch path to keep in sync.
 *
 * Finding "Important 7" (task-15a-report.md fix round 1): a signed-in
 * user who belongs to zero organizations used to wait on "Loading…"
 * forever — `orgs.data` resolves, `firstOrg` is undefined, and the effect
 * below simply had nothing further to do. That state now renders a
 * friendly message instead of hanging.
 */
export function RootRedirect() {
  const navigate = useNavigate();
  const t = useT();
  const setupStatus = useSetupStatus();
  // useSettledMe stays disabled until setup is known to be complete, so
  // this page does not fetch /auth/me itself on a fresh instance. It is
  // not the only reader of ['me']: the I18nProvider fetches the same query
  // on every load to learn the user's language, so on a fresh instance
  // that 401 lands in the shared cache and `me.isError` turns true here
  // whether or not this hook is enabled. What stops it racing the setup
  // redirect is the order in the effect below: it waits for the setup
  // status and acts on `completed` before it ever looks at `me.isError`.
  //
  // It redirects only on an answer fetched since this mount (useSettledMe):
  // a cached one can be from before the session changed — a password just
  // replaced still reads must_change_password — and would bounce the
  // session through a screen it no longer belongs on. Only a 401 means
  // there is no session; any other failure (offline, a 500) says nothing
  // about it, so it is shown rather than sent to /login.
  const { me, settled, failed } = useSettledMe({ enabled: setupStatus.data?.completed === true });
  const signedOut = settled && me.isError && errorStatus(me.error) === 401;
  const ready = settled && !failed && me.data?.mfa_complete === true && !me.data.must_change_password;
  const orgs = useOrgs({ enabled: ready });

  useEffect(() => {
    if (!setupStatus.data) return;
    if (!setupStatus.data.completed) {
      void navigate({ to: '/setup' });
      return;
    }
    if (!settled) return;
    if (signedOut) {
      void navigate({ to: '/login', search: { step: undefined } });
      return;
    }
    if (failed || !me.data) return;
    if (!me.data.mfa_complete) {
      if (me.data.mfa_enrolment_required) {
        void navigate({ to: '/account/enrol' });
      } else {
        void navigate({ to: '/login', search: { step: 'totp' } });
      }
      return;
    }
    if (me.data.must_change_password) {
      void navigate({ to: '/account/password' });
      return;
    }
    if (!orgs.data) return;
    const firstOrg = orgs.data.orgs?.[0];
    if (firstOrg) {
      void navigate({ to: '/o/$org/hosts', params: { org: firstOrg.slug }, search: { q: undefined } });
    }
  }, [navigate, setupStatus.data, settled, signedOut, failed, me.data, orgs.data]);

  if (setupStatus.isError) return <QueryError error={setupStatus.error} />;
  if (setupStatus.data?.completed && failed && !signedOut) return <QueryError error={me.error} />;
  if (ready) {
    if (orgs.isError) return <QueryError error={orgs.error} />;
    if (orgs.data && !orgs.data.orgs?.length) {
      return (
        <QueryError error={new Error(t.routes.noOrganizationInvite)} />
      );
    }
  }

  return <QueryLoading />;
}
