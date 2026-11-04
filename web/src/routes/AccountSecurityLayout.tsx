import { useNavigate } from '@tanstack/react-router';
import { AppShell } from '../ui/shell.tsx';
import { QueryError, QueryLoading } from '../ui/QueryState.tsx';
import { useQueryGuard } from '../ui/useQueryGuard.tsx';
import { useMe } from '../api/hooks/useMe.ts';
import { useOrgs } from '../api/hooks/useOrgs.ts';
import { usePasskeys } from '../api/hooks/usePasskeys.ts';
import { logout } from '../api/hooks/useAuth.ts';
import { useResetSession } from '../lib/session.ts';
import { useT } from '../i18n/useT.ts';
import { AccountSecurityPage } from '../features/account/AccountSecurityPage.tsx';
import { sessionState } from '../lib/sessionState.ts';
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
  const t = useT();
  const me = useMe({ enabled: true });
  // GET /orgs is refused to a session that is not MFA-complete or must
  // change its password; such a session is sent on to enrolment,
  // verification or the password change instead (below).
  const state = me.data ? sessionState(me.data) : undefined;
  const orgs = useOrgs({ enabled: state === 'complete' });
  const passkeys = usePasskeys();

  const meGuard = useQueryGuard(me);
  const orgsGuard = useQueryGuard(orgs);
  if (meGuard) return meGuard;
  if (state && state !== 'complete') return <PendingSessionRedirect state={state} />;
  if (orgsGuard) return orgsGuard;

  const firstOrg = orgs.data?.orgs?.[0];
  if (!me.data || !orgs.data) return <QueryLoading />;
  if (!firstOrg) {
    return <QueryError error={new Error(t.routes.noOrganization)} />;
  }

  // Same derivation as AccountSecurityPage's own header pill (Important
  // 4): mfa_complete alone cannot tell "no factor at all" apart from "a
  // real factor, confirmed".
  const hasSecondFactor = me.data.totp_confirmed === true || (passkeys.data?.passkeys ?? []).length > 0;

  return (
    <AppShell
      org={{ slug: firstOrg.slug, name: firstOrg.name, role: firstOrg.role }}
      orgs={(orgs.data.orgs ?? []).map((o) => ({ slug: o.slug, name: o.name }))}
      onSelectOrg={(slug) => void navigate({ to: '/o/$org/hosts', params: { org: slug }, search: { q: undefined } })}
      crumbs={
        <>
          <span>{t.routes.accountSecurity.crumbAccount}</span>
          <span className="text-border">/</span>
          <span className="font-semibold text-foreground">{t.routes.accountSecurity.crumbSecurity}</span>
        </>
      }
      user={{
        displayName: me.data.user.display_name,
        subtitle: hasSecondFactor ? t.routes.accountSecurity.secondFactorActive : t.routes.accountSecurity.secondFactorRequired,
      }}
      onLogout={async () => {
        await logout();
        // Navigate first, then drop every cached query and mutation
        // (lib/session.ts): whoever signs in next on this tab must not
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
