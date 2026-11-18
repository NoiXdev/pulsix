import { Outlet, useRouterState } from '@tanstack/react-router';
import { VersionWatcher } from '../features/version/VersionWatcher.tsx';
import { ExpiryNotice } from '../features/session/ExpiryNotice.tsx';
import { AppShell } from '../ui/shell.tsx';
import { QueryLoading } from '../ui/QueryState.tsx';
import { useQueryGuard } from '../ui/useQueryGuard.tsx';
import { useMe } from '../api/hooks/useMe.ts';
import { useOrgs } from '../api/hooks/useOrgs.ts';
import { usePasskeys } from '../api/hooks/usePasskeys.ts';
import { useSignOut } from '../lib/session.ts';
import { lastOrg } from '../lib/lastOrg.ts';
import { useT } from '../i18n/useT.ts';
import { roleLabel } from '../i18n/roleLabel.ts';
import { useLockAction } from '../features/lock/useLockAction.tsx';
import { sessionState } from '../lib/sessionState.ts';
import { PendingSessionRedirect } from './PendingSessionRedirect.tsx';

/**
 * AccountLayout is the personal settings area (/account/*): the app shell
 * with the settings navigation instead of the organization's — a "Back to
 * <organization>" link, the person's name and role, and the area's pages
 * (Security, Appearance, Profile, API tokens, My activity), which render in the
 * <Outlet/>. No route param names an organization here: Back goes to the
 * one the person came from (lib/lastOrg) while they are still a member,
 * else to their first; a person without any gets no Back link and the pages
 * still open. The pages that must come first for a session that
 * is not complete (/account/enrol, /password, /lock-pin) are not part of
 * this layout and render without the shell.
 */
export function AccountLayout() {
  const signOut = useSignOut();
  const t = useT();
  const lockAction = useLockAction();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
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

  const list = orgs.data?.orgs ?? [];
  if (!me.data || !orgs.data) return <QueryLoading />;
  const remembered = lastOrg();
  const backOrg = list.find((o) => o.slug === remembered) ?? list[0];

  // Same derivation as AccountSecurityPage's own header pill (Important
  // 4): mfa_complete alone cannot tell "no factor at all" apart from "a
  // real factor, confirmed".
  const hasSecondFactor = me.data.totp_confirmed === true || (passkeys.data?.passkeys ?? []).length > 0;

  const pages: Record<string, string> = {
    security: t.account.nav.security,
    appearance: t.account.nav.appearance,
    profile: t.account.nav.profile,
    tokens: t.account.nav.tokens,
    activity: t.account.nav.activity,
  };
  const segment = pathname.split('/').filter(Boolean)[1] ?? '';
  const page = Object.hasOwn(pages, segment) ? pages[segment] : undefined;

  return (
    <AppShell
      notice={
        <>
          <ExpiryNotice />
          <VersionWatcher />
        </>
      }
      org={backOrg && { slug: backOrg.slug, name: backOrg.name, role: backOrg.role }}
      orgs={[]}
      onSelectOrg={() => {}}
      settingsNav={{
        kind: 'account',
        backOrg: backOrg && { slug: backOrg.slug, name: backOrg.name },
        personName: me.data.user.display_name,
        personRole: backOrg && t.account.nav.roleIn(roleLabel(t, backOrg.role), backOrg.name),
      }}
      onLockSession={lockAction.lock}
      crumbs={
        <>
          <span>{t.account.nav.label}</span>
          {page && (
            <>
              <span className="text-border">/</span>
              <span className="font-semibold text-foreground">{page}</span>
            </>
          )}
        </>
      }
      user={{
        displayName: me.data.user.display_name,
        subtitle: hasSecondFactor ? t.routes.accountSecurity.secondFactorActive : t.routes.accountSecurity.secondFactorRequired,
      }}
      onLogout={signOut}
    >
      <Outlet />
      {lockAction.dialog}
    </AppShell>
  );
}
