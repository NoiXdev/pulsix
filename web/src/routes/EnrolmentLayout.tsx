import { useState } from 'react';
import { Navigate, useNavigate } from '@tanstack/react-router';
import { LogOut } from 'lucide-react';
import { BarePage } from '../ui/bare-page.tsx';
import { Button } from '../ui/button.tsx';
import { QueryLoading } from '../ui/QueryState.tsx';
import { useQueryGuard } from '../ui/useQueryGuard.tsx';
import { useRefetchMe, useSettledMe } from '../api/hooks/useMe.ts';
import { useSignOut } from '../lib/session.ts';
import { useT } from '../i18n/useT.ts';
import { AccountSecurityPage } from '../features/account/AccountSecurityPage.tsx';

/**
 * EnrolmentLayout is /account/enrol: where a session lands whose account
 * has no second factor yet — after a password login, after the first-run
 * wizard, after accepting an invitation for a new account. A second factor
 * is mandatory, so the API admits such a session to nothing but the
 * enrolment routes, /auth/me and logout; this page therefore renders
 * without the app shell (whose organization switcher it could not load)
 * and offers exactly the two ways to enrol, plus signing out.
 *
 * Confirming a TOTP secret or registering a passkey completes the session
 * on the server with no fresh login. The page decides once, on the first
 * /auth/me fetched since it mounted (not a cached one), whether it is
 * serving an enrolment at all, and keeps that decision: the /auth/me
 * refetch that follows a confirmed TOTP secret reports the session
 * complete, but the recovery codes are still on screen and must stay
 * there until the user has saved them — only then (AccountSecurityPage's
 * onEnrolled) does it continue into the app.
 *
 * An account that must also change its password (an administrator reset
 * its second factor and set a temporary password) is refused everything
 * but /auth/me and the change once enrolment completes the session — the
 * passkey list refetched after a registration included. The page does not
 * act on that refusal (nor does RootLayout, while it is here): it reads
 * the passkey list only for its status pill, and continues to the root
 * redirect, which sends the session on to /account/password.
 */
export function EnrolmentLayout() {
  const navigate = useNavigate();
  const signOut = useSignOut();
  const t = useT();
  const refetchMe = useRefetchMe();
  const { me, settled } = useSettledMe({ enabled: true });
  const meGuard = useQueryGuard(me);

  // Decided on an answer fetched since this mount (useSettledMe), never on
  // a cached one from before the session changed.
  const [enrolling, setEnrolling] = useState<boolean | null>(null);
  if (enrolling === null && settled && me.data) {
    setEnrolling(me.data.mfa_enrolment_required);
  }

  if (meGuard) return meGuard;
  if (!me.data || enrolling === null) return <QueryLoading />;
  // Reached by a session with nothing to enrol: the root redirect knows
  // where it belongs (the app, or the TOTP step of /login).
  if (!enrolling) return <Navigate to="/" />;

  return (
    <BarePage
      action={
        <Button type="button" onClick={() => void signOut()}>
          <LogOut size={14} /> {t.routes.enrolment.signOut}
        </Button>
      }
    >
      <div className="flex flex-col gap-1.5">
        <h1 className="text-xl font-semibold">{t.routes.enrolment.title}</h1>
        <p className="text-muted-foreground">
          {t.routes.enrolment.signedInAs(me.data.user.email)} {t.routes.enrolment.intro}
        </p>
      </div>
      <AccountSecurityPage
        onEnrolled={async () => {
          await refetchMe();
          await navigate({ to: '/' });
        }}
      />
    </BarePage>
  );
}
