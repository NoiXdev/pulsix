import { SidebarFacts } from '../../ui/detail-layout.tsx';
import type { AdminUserDetail } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * AccountSignIn is an account's Sign-in section body: which second factors
 * it has and how many sessions are signed in. Shared by the account page
 * and, for an instance admin, the member page.
 */
export function AccountSignIn({ user }: { user: AdminUserDetail }) {
  const t = useT();
  const s = t.users.signIn;
  return (
    <SidebarFacts
      items={[
        { label: s.authenticator, value: user.factors.totp ? s.authenticatorOn : s.authenticatorOff },
        { label: s.passkeys, value: user.factors.passkeys },
        { label: s.recoveryCodes, value: user.factors.recovery_codes_left },
        { label: s.sessions, value: user.active_sessions },
      ]}
    />
  );
}
