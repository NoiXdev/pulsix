import { useEffect } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { useSetupStatus } from '../api/hooks/useSetupStatus.ts';
import { useMe } from '../api/hooks/useMe.ts';
import { useOrgs } from '../api/hooks/useOrgs.ts';

/**
 * RootRedirect resolves the four startup states the task brief names,
 * strictly in this order:
 *
 *   1. setup not completed  -> /setup
 *   2. no session           -> /login
 *   3. session, MFA pending -> /login?step=totp
 *   4. ready session        -> /o/{first org}/hosts
 *
 * It calls useSetupStatus and useMe directly (not a router loader) so the
 * same TanStack Query cache both hooks share is what every other page
 * reads from too — no separate fetch path to keep in sync.
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
      void navigate({ to: '/login', search: { step: 'totp' } });
      return;
    }
    if (!orgs.data) return;
    const firstOrg = orgs.data.orgs?.[0];
    if (firstOrg) {
      void navigate({ to: '/o/$org/hosts', params: { org: firstOrg.slug } });
    }
  }, [navigate, setupStatus.data, me.data, me.isError, orgs.data]);

  return (
    <div className="flex h-screen items-center justify-center bg-background text-muted-foreground">
      Loading…
    </div>
  );
}
