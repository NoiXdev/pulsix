import { useEffect } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useSetupStatus } from '../api/hooks/useSetupStatus.ts';
import { useMe } from '../api/hooks/useMe.ts';
import { useOrgs } from '../api/hooks/useOrgs.ts';
import { QueryError, QueryLoading } from '../ui/QueryState.tsx';

/**
 * RootRedirect resolves the four startup states the task brief names,
 * strictly in this order:
 *
 *   1. setup not completed          -> /setup
 *   2. no session                   -> /login
 *   3. session, factor to verify    -> /login?step=totp
 *   4. session, no factor yet       -> /account/enrol (a second factor is
 *                                      mandatory; nothing else is reachable)
 *   5. ready session                -> /o/{first org}/hosts
 *
 * It calls useSetupStatus and useMe directly (not a router loader) so the
 * same TanStack Query cache both hooks share is what every other page
 * reads from too — no separate fetch path to keep in sync.
 *
 * Finding "Important 7" (task-15a-report.md fix round 1): a signed-in
 * user who belongs to zero organizations used to wait on "Loading…"
 * forever — `orgs.data` resolves, `firstOrg` is undefined, and the effect
 * below simply had nothing further to do. That state now renders a
 * friendly message instead of hanging.
 */
export function RootRedirect() {
  const navigate = useNavigate();
  const setupStatus = useSetupStatus();
  // useMe is disabled until setup is known to be complete: on a fresh
  // instance /auth/me would just 401 (no session can exist yet), and that
  // failure would otherwise race the setup-status redirect below.
  const me = useMe({ enabled: setupStatus.data?.completed === true });
  const orgs = useOrgs({ enabled: me.data?.mfa_complete === true });

  useEffect(() => {
    if (!setupStatus.data) return;
    if (!setupStatus.data.completed) {
      void navigate({ to: '/setup' });
      return;
    }
    if (me.isError) {
      void navigate({ to: '/login', search: { step: undefined } });
      return;
    }
    if (!me.data) return;
    if (!me.data.mfa_complete) {
      if (me.data.mfa_enrolment_required) {
        void navigate({ to: '/account/enrol' });
      } else {
        void navigate({ to: '/login', search: { step: 'totp' } });
      }
      return;
    }
    if (!orgs.data) return;
    const firstOrg = orgs.data.orgs?.[0];
    if (firstOrg) {
      void navigate({ to: '/o/$org/hosts', params: { org: firstOrg.slug }, search: { q: undefined } });
    }
  }, [navigate, setupStatus.data, me.data, me.isError, orgs.data]);

  if (setupStatus.isError) return <QueryError error={setupStatus.error} />;
  if (me.data?.mfa_complete) {
    if (orgs.isError) return <QueryError error={orgs.error} />;
    if (orgs.data && !orgs.data.orgs?.length) {
      return (
        <QueryError
          error={new Error('This account does not belong to any organization yet. Ask an administrator to invite you.')}
        />
      );
    }
  }

  return <QueryLoading />;
}
