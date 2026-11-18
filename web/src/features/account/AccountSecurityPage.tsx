import { SettingsPage } from '../../ui/settings.tsx';
import { useT } from '../../i18n/useT.ts';
import { SignInCard } from './SignInCard.tsx';
import { SessionCard } from './SessionCard.tsx';

/**
 * AccountSecurityPage is Settings > Security: the "Sign-in" card (password,
 * authenticator app, recovery codes, passkeys) and the "Session" card (lock
 * PIN, this session). The mockup's session list is left out: the API has no
 * route to list one's own sessions.
 *
 * The page serves two layouts: the account security page inside the app
 * shell (AccountLayout) and second-factor enrolment for an account that has
 * none yet (EnrolmentLayout, which passes onEnrolled). Enrolment shows the
 * Sign-in card alone; see SignInCard for what onEnrolled does.
 */
export function AccountSecurityPage({ onEnrolled }: { onEnrolled?: () => void | Promise<void> } = {}) {
  const t = useT();
  return (
    // Enrolment has one job and no Session card: no description there.
    <SettingsPage title={t.account.title} description={onEnrolled ? undefined : t.account.description}>
      <SignInCard onEnrolled={onEnrolled} />
      {!onEnrolled && <SessionCard />}
    </SettingsPage>
  );
}
