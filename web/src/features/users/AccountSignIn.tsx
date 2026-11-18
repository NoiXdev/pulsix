import type { AdminUserDetail } from '../../api/types.ts';
import { SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import { useT } from '../../i18n/useT.ts';

/**
 * AccountSignIn is an account's Sign-in card: which second factors it has
 * and how many sessions are signed in. Shared by the account page and, for
 * an instance admin, the member page.
 */
export function AccountSignIn({ user }: { user: AdminUserDetail }) {
  const t = useT();
  const s = t.users.signIn;
  return (
    <SettingsCard title={s.title} description={s.description}>
      <SettingsRow label={s.authenticator}>{user.factors.totp ? s.authenticatorOn : s.authenticatorOff}</SettingsRow>
      <SettingsRow label={s.passkeys}>{user.factors.passkeys}</SettingsRow>
      <SettingsRow label={s.recoveryCodes}>{user.factors.recovery_codes_left}</SettingsRow>
      <SettingsRow label={s.sessions}>{user.active_sessions}</SettingsRow>
    </SettingsCard>
  );
}
