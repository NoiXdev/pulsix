import { useNavigate } from '@tanstack/react-router';
import { AppShell } from '../ui/shell.tsx';
import { QueryError, QueryLoading } from '../ui/QueryState.tsx';
import { useQueryGuard } from '../ui/useQueryGuard.tsx';
import { useMe } from '../api/hooks/useMe.ts';
import { useOrgs } from '../api/hooks/useOrgs.ts';
import { usePasskeys } from '../api/hooks/usePasskeys.ts';
import { logout } from '../api/hooks/useAuth.ts';
import { useResetSession } from '../api/session.ts';
import { AccountSecurityPage } from '../features/account/AccountSecurityPage.tsx';
import { PendingSessionRedirect } from './PendingSessionRedirect.tsx';

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
  const navigate = useNavigate();
  const resetSession = useResetSession();
  const me = useMe({ enabled: true });
  // GET /orgs is refused to a session that is not MFA-complete; such a
  // session is sent on to enrolment or verification instead (below).
  const orgs = useOrgs({ enabled: me.data?.mfa_complete === true });
  const passkeys = usePasskeys();

  const meGuard = useQueryGuard(me);
  const orgsGuard = useQueryGuard(orgs);
  if (meGuard) return meGuard;
  if (me.data && !me.data.mfa_complete) {
    return <PendingSessionRedirect state={me.data.mfa_enrolment_required ? 'enrol' : 'verify'} />;
  }
  if (orgsGuard) return orgsGuard;

  const firstOrg = orgs.data?.orgs?.[0];
  if (!me.data || !orgs.data) return <QueryLoading />;
  if (!firstOrg) {
    return <QueryError error={new Error('This account does not belong to any organization yet.')} />;
  }

  // Same derivation as AccountSecurityPage's own header pill (Important
  // 4): mfa_complete alone cannot tell "no factor at all" apart from "a
  // real factor, confirmed".
  const hasSecondFactor = me.data.totp_confirmed === true || (passkeys.data?.passkeys ?? []).length > 0;

  return (
    <AppShell
      org={{ slug: firstOrg.slug, name: firstOrg.name }}
      orgs={(orgs.data.orgs ?? []).map((o) => ({ slug: o.slug, name: o.name }))}
      onSelectOrg={(slug) => void navigate({ to: '/o/$org/hosts', params: { org: slug }, search: { q: undefined } })}
      crumbs={
        <>
          <span>Account</span>
          <span className="text-border">/</span>
          <span className="font-semibold text-foreground">Security</span>
        </>
      }
      user={{
        displayName: me.data.user.display_name,
        subtitle: hasSecondFactor ? 'Second factor active' : 'Second factor required',
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
      <AccountSecurityPage />
    </AppShell>
  );
}
