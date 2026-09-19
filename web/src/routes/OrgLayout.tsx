import { ChevronRight } from 'lucide-react';
import { Outlet, useNavigate, useParams, useRouterState } from '@tanstack/react-router';
import { AppShell } from '../ui/shell.tsx';
import { QueryLoading } from '../ui/QueryState.tsx';
import { useQueryGuard } from '../ui/useQueryGuard.tsx';
import { useOrg, useOrgs } from '../api/hooks/useOrgs.ts';
import { useMe } from '../api/hooks/useMe.ts';
import { logout } from '../api/hooks/useAuth.ts';
import { useResetSession } from '../api/session.ts';
import { crumbLabels } from '../lib/breadcrumbs.ts';
import { PendingSessionRedirect } from './PendingSessionRedirect.tsx';

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
  const resetSession = useResetSession();

  const me = useMe({ enabled: true });
  // A session that is not MFA-complete is refused every organization
  // route, so these wait until /auth/me says it is complete and the
  // session is sent on to enrolment or verification instead (below).
  const complete = me.data?.mfa_complete === true;
  const org = useOrg(orgSlug, { enabled: complete });
  const orgs = useOrgs({ enabled: complete });

  const meGuard = useQueryGuard(me);
  const orgGuard = useQueryGuard(org);
  const orgsGuard = useQueryGuard(orgs);
  if (meGuard) return meGuard;
  if (me.data && !me.data.mfa_complete) {
    return <PendingSessionRedirect state={me.data.mfa_enrolment_required ? 'enrol' : 'verify'} />;
  }
  if (orgGuard) return orgGuard;
  if (orgsGuard) return orgsGuard;
  if (!org.data || !me.data || !orgs.data) return <QueryLoading />;

  const crumbs = crumbLabels(pathname, orgSlug);

  return (
    <AppShell
      org={{ slug: orgSlug, name: org.data.org.name, role: org.data.org.role }}
      orgs={(orgs.data.orgs ?? []).map((o) => ({ slug: o.slug, name: o.name }))}
      onSelectOrg={(slug) => {
        if (slug === orgSlug) return;
        void navigate({ to: '/o/$org/hosts', params: { org: slug }, search: { q: undefined } });
      }}
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
        subtitle: `${org.data.org.role.charAt(0).toUpperCase()}${org.data.org.role.slice(1)}`,
      }}
      onLogout={async () => {
        await logout();
        // Navigate first, then drop every cached query and mutation
        // (api/session.ts): whoever signs in next on this tab must not
        // be served this user's data, and resetting while this layout
        // is still mounted would only make it re-fetch everything with
        // the now-dead session.
        await navigate({ to: '/login', search: { step: undefined } });
        resetSession();
      }}
    >
      <Outlet />
    </AppShell>
  );
}
