import { AppShell } from '../ui/shell.tsx';
import { useMe } from '../api/hooks/useMe.ts';
import { useOrgs } from '../api/hooks/useOrgs.ts';
import { AccountSecurityPage } from '../features/account/AccountSecurityPage.tsx';

/**
 * AccountSecurityLayout renders /account/security inside the same
 * AppShell every org-scoped page uses — the mockup's own ACCOUNT SECURITY
 * artboard keeps the sidebar and org switcher visible even though this
 * page is account-level, not org-scoped (see the shell() call in
 * design/mockups/build.mjs for that artboard). It picks the caller's
 * first organization purely so the shell has something to show; unlike
 * OrgLayout, no route param names an org here.
 */
export function AccountSecurityLayout() {
  const me = useMe({ enabled: true });
  const orgs = useOrgs({ enabled: true });

  const firstOrg = orgs.data?.orgs?.[0];

  if (!me.data || !firstOrg) {
    return <div className="flex h-screen items-center justify-center bg-background text-muted-foreground">Loading…</div>;
  }

  return (
    <AppShell
      org={{ slug: firstOrg.slug, name: firstOrg.name }}
      active={null}
      crumbs={
        <>
          <span>Account</span>
          <span className="text-border">/</span>
          <span className="font-semibold text-foreground">Security</span>
        </>
      }
      user={{
        displayName: me.data.user.display_name,
        subtitle: me.data.mfa_complete ? 'Second factor active' : 'Second factor required',
      }}
    >
      <AccountSecurityPage />
    </AppShell>
  );
}
