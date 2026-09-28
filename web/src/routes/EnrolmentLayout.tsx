import { useState } from 'react';
import { Navigate, useNavigate } from '@tanstack/react-router';
import { LogOut } from 'lucide-react';
import { PulsixMark } from '../ui/PulsixMark.tsx';
import { Button } from '../ui/button.tsx';
import { QueryLoading } from '../ui/QueryState.tsx';
import { useQueryGuard } from '../ui/useQueryGuard.tsx';
import { useMe, useRefetchMe } from '../api/hooks/useMe.ts';
import { logout } from '../api/hooks/useAuth.ts';
import { useResetSession } from '../lib/session.ts';
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
 * on the server with no fresh login. The page decides once, on its first
 * /auth/me, whether it is serving an enrolment at all, and keeps that
 * decision: the /auth/me refetch that follows a confirmed TOTP secret
 * reports the session complete, but the recovery codes are still on
 * screen and must stay there until the user has saved them — only then
 * (AccountSecurityPage's onEnrolled) does it continue into the app.
 */
export function EnrolmentLayout() {
  const navigate = useNavigate();
  const resetSession = useResetSession();
  const refetchMe = useRefetchMe();
  const me = useMe({ enabled: true });
  const meGuard = useQueryGuard(me);

  const [enrolling, setEnrolling] = useState<boolean | null>(null);
  if (enrolling === null && me.data) {
    setEnrolling(me.data.mfa_enrolment_required);
  }

  if (meGuard) return meGuard;
  if (!me.data || enrolling === null) return <QueryLoading />;
  // Reached by a session with nothing to enrol: the root redirect knows
  // where it belongs (the app, or the TOTP step of /login).
  if (!enrolling) return <Navigate to="/" />;

  return (
    <div className="min-h-screen bg-background text-foreground text-[13px]">
      <header className="flex items-center justify-between border-b border-border px-8 py-4">
        <div className="flex items-center gap-3">
          <PulsixMark size={28} />
          <span className="font-mono text-xl font-extrabold tracking-tight">
            pulsi<span className="text-brand">x</span>
          </span>
        </div>
        <Button
          type="button"
          onClick={async () => {
            await logout();
            await navigate({ to: '/login', search: { step: undefined } });
            resetSession();
          }}
        >
          <LogOut size={14} /> Sign out
        </Button>
      </header>
      <main className="mx-auto flex max-w-[980px] flex-col gap-5 px-8 py-8">
        <div className="flex flex-col gap-1.5">
          <h1 className="text-xl font-semibold">Set up a second factor</h1>
          <p className="text-muted-foreground">
            Signed in as {me.data.user.email}. Every account needs an authenticator app or a passkey before it can
            work inside an organization — add one now to continue.
          </p>
        </div>
        <AccountSecurityPage
          onEnrolled={async () => {
            await refetchMe();
            await navigate({ to: '/' });
          }}
        />
      </main>
    </div>
  );
}
