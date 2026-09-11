import { Outlet, useParams, useRouterState } from '@tanstack/react-router';
import { AppShell, type NavKey } from '../ui/shell.tsx';
import { useOrg } from '../api/hooks/useOrgs.ts';
import { useMe } from '../api/hooks/useMe.ts';

const NAV_BY_PREFIX: Array<[string, NavKey]> = [
  ['/settings/tokens', 'tokens'],
  ['/hosts', 'hosts'],
  ['/members', 'members'],
  ['/activity', 'activity'],
];

/** OrgLayout is the route-connected wrapper around the pure AppShell
 * component (src/ui/shell.tsx): it resolves the current org and user from
 * the API and figures out which nav item is active from the URL, then
 * renders every /o/$org/* child route inside the shell via <Outlet/>. */
export function OrgLayout() {
  const { org: orgSlug } = useParams({ strict: false }) as { org: string };
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const org = useOrg(orgSlug);
  const me = useMe({ enabled: true });

  const active = NAV_BY_PREFIX.find(([suffix]) => pathname.includes(suffix))?.[1] ?? null;

  if (!org.data || !me.data) {
    return <div className="flex h-screen items-center justify-center bg-background text-muted-foreground">Loading…</div>;
  }

  return (
    <AppShell
      org={{ slug: orgSlug, name: org.data.org.name }}
      active={active}
      crumbs={<span className="font-semibold text-foreground capitalize">{active ?? org.data.org.name}</span>}
      user={{
        displayName: me.data.user.display_name,
        subtitle: `${org.data.org.role.charAt(0).toUpperCase()}${org.data.org.role.slice(1)}`,
      }}
    >
      <Outlet />
    </AppShell>
  );
}
