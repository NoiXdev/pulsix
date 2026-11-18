import { useEffect, useState } from 'react';
import { ChevronRight } from 'lucide-react';
import { Outlet, useNavigate, useParams, useRouterState } from '@tanstack/react-router';
import { VersionWatcher } from '../features/version/VersionWatcher.tsx';
import { ExpiryNotice } from '../features/session/ExpiryNotice.tsx';
import { AppShell } from '../ui/shell.tsx';
import { QueryLoading } from '../ui/QueryState.tsx';
import { useQueryGuard } from '../ui/useQueryGuard.tsx';
import { useOrg, useOrgs } from '../api/hooks/useOrgs.ts';
import { useHost } from '../api/hooks/useHosts.ts';
import { useMe } from '../api/hooks/useMe.ts';
import { useSetupStatus } from '../api/hooks/useSetupStatus.ts';
import { usePendingRunRequestCount } from '../api/hooks/useRunRequests.ts';
import { useSignOut } from '../lib/session.ts';
import { crumbLabels, hostIdInPath } from '../lib/breadcrumbs.ts';
import { rememberOrg } from '../lib/lastOrg.ts';
import { may } from '../lib/roles.ts';
import { useT } from '../i18n/useT.ts';
import { useLockAction } from '../features/lock/useLockAction.tsx';
import { roleLabel } from '../i18n/roleLabel.ts';
import { sessionState } from '../lib/sessionState.ts';
import { PendingSessionRedirect } from './PendingSessionRedirect.tsx';
import { CreateOrgDialog } from '../features/orgs/CreateOrgDialog.tsx';

/** OrgLayout is the route-connected wrapper around the pure AppShell
 * component (src/ui/shell.tsx): it resolves the current org, the user's
 * full org list (for the switcher) and the user from the API, derives the
 * breadcrumb trail from the real URL, and renders every /o/$org/* child
 * route inside the shell via <Outlet/>.
 *
 * Error handling (finding "Important 7"): org/me/orgs used to have no
 * error branch at all, so any failure — including a 401 from a session
 * that had already expired — left the page on "Loading…" forever. Now a
 * first-load failure renders through the shared QueryError component via
 * useQueryGuard; a transient background refetch failure (data already
 * present) keeps this page up and only toasts (fix round 2), while a
 * 401/403/404 on a refetch is final and replaces the page — see
 * ui/useQueryGuard.tsx. */
export function OrgLayout() {
  const { org: orgSlug } = useParams({ strict: false }) as { org: string };
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const navigate = useNavigate();
  const signOut = useSignOut();
  const t = useT();
  const lockAction = useLockAction();
  const [creatingOrg, setCreatingOrg] = useState(false);

  const me = useMe({ enabled: true });
  // The build version shown in the shell. The status is public and already
  // cached from the first load (RootRedirect); the shell does not wait for it.
  const setupStatus = useSetupStatus();
  // A session that is not MFA-complete, or whose account must change its
  // password, is refused every organization route, so these wait until
  // /auth/me says it is complete and the session is sent on to enrolment,
  // verification or the password change instead (below).
  const state = me.data ? sessionState(me.data) : undefined;
  const complete = state === 'complete';
  const org = useOrg(orgSlug, { enabled: complete });
  const orgs = useOrgs({ enabled: complete });
  // The pending run requests beside Runs, polled; a failed read just shows
  // no badge rather than standing in the way of the page.
  const pendingRunRequests = usePendingRunRequestCount(orgSlug, { enabled: complete });
  // A host page's crumb is the host's name: the same query the page reads,
  // so it costs no extra request. Until it answers the crumb is the id.
  const hostId = hostIdInPath(pathname, orgSlug);
  const crumbHost = useHost(orgSlug, complete && hostId ? hostId : '');

  // The personal settings area is outside any organization; its "Back"
  // link returns to the one this person was in.
  const orgLoaded = !!org.data;
  useEffect(() => {
    if (orgLoaded) rememberOrg(orgSlug);
  }, [orgLoaded, orgSlug]);

  const meGuard = useQueryGuard(me);
  const orgGuard = useQueryGuard(org);
  const orgsGuard = useQueryGuard(orgs);
  if (meGuard) return meGuard;
  if (state && state !== 'complete') return <PendingSessionRedirect state={state} />;
  if (orgGuard) return orgGuard;
  if (orgsGuard) return orgsGuard;
  if (!org.data || !me.data || !orgs.data) return <QueryLoading />;

  const crumbNames: Record<string, string> = hostId && crumbHost.data ? { [`hosts/${hostId}`]: crumbHost.data.host.name } : {};
  const crumbs = crumbLabels(pathname, orgSlug, t, crumbNames);
  const inSettings = pathname === `/o/${orgSlug}/settings` || pathname.startsWith(`/o/${orgSlug}/settings/`);
  const settingsNav = inSettings
    ? {
        kind: 'settings' as const,
        backOrg: { slug: orgSlug, name: org.data.org.name },
        showAlerting: may(org.data.org.role, 'channels.read'),
        showInstance: me.data.instance_admin === true,
      }
    : undefined;

  return (
    <AppShell
      notice={
        <>
          <ExpiryNotice />
          <VersionWatcher />
        </>
      }
      org={{ slug: orgSlug, name: org.data.org.name, role: org.data.org.role }}
      orgs={(orgs.data.orgs ?? []).map((o) => ({ slug: o.slug, name: o.name }))}
      onSelectOrg={(slug) => {
        if (slug === orgSlug) return;
        void navigate({ to: '/o/$org/hosts', params: { org: slug }, search: { q: undefined } });
      }}
      settingsNav={settingsNav}
      onCreateOrg={me.data.instance_admin ? () => setCreatingOrg(true) : undefined}
      onLockSession={lockAction.lock}
      crumbs={
        <>
          {crumbs.map((label, i) => (
            <span key={i} className="flex items-center gap-2">
              {i > 0 && <ChevronRight size={13} className="text-border" />}
              <span className={i === crumbs.length - 1 ? 'font-semibold text-foreground' : undefined}>{label}</span>
            </span>
          ))}
        </>
      }
      user={{
        displayName: me.data.user.display_name,
        subtitle: roleLabel(t, org.data.org.role),
      }}
      onLogout={signOut}
      version={setupStatus.data?.version}
      pendingRunRequests={pendingRunRequests}
    >
      <Outlet />
      {lockAction.dialog}
      <CreateOrgDialog open={creatingOrg} onClose={() => setCreatingOrg(false)} />
    </AppShell>
  );
}
