import { Navigate, useNavigate } from '@tanstack/react-router';
import { LogOut } from 'lucide-react';
import { BarePage } from '../../ui/bare-page.tsx';
import { Button } from '../../ui/button.tsx';
import { Card } from '../../ui/table.tsx';
import { QueryError, QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useRefetchMe, useSettledMe } from '../../api/hooks/useMe.ts';
import { useSignOut } from '../../lib/session.ts';
import { sessionState } from '../../lib/sessionState.ts';
import { useT } from '../../i18n/useT.ts';
import { PasswordChangeForm } from './PasswordChangeForm.tsx';

/**
 * ChangePasswordPage is /account/password: where a session lands whose
 * account must replace a password an administrator set (backlog 7.74).
 * The API admits such a session to nothing but this change, /auth/me and
 * logout, so the page renders without the app shell, like second-factor
 * enrolment, and offers exactly the change and signing out.
 *
 * The change takes the temporary password and a new one, which must
 * differ. Once the server has it, /auth/me is fetched afresh before
 * continuing to the root redirect, so that does not read the flag from
 * the cache and send the session straight back here.
 *
 * Any other session goes to the root redirect, which knows where it
 * belongs: enrolment or the second-factor step first, else the app.
 */
export function ChangePasswordPage() {
  const t = useT();
  const navigate = useNavigate();
  const signOut = useSignOut();
  const refetchMe = useRefetchMe();
  const { me, settled, failed } = useSettledMe({ enabled: true });
  // While the fetch on mount has failed, `data` is a stale cached answer:
  // the failure is shown, and the guard does not also toast about it.
  const meGuard = useQueryGuard(me, { enabled: !failed });

  if (failed) return <QueryError error={me.error} />;
  if (meGuard) return meGuard;
  if (!me.data || !settled) return <QueryLoading />;
  if (sessionState(me.data) !== 'password') return <Navigate to="/" />;

  return (
    <BarePage
      action={
        <Button type="button" onClick={() => void signOut()}>
          <LogOut size={14} /> {t.account.forced.signOut}
        </Button>
      }
    >
      <div className="flex flex-col gap-1.5">
        <h1 className="text-xl font-semibold">{t.account.forced.title}</h1>
        <p className="text-muted-foreground">{t.account.forced.intro(me.data.user.email)}</p>
      </div>
      <Card className="max-w-[420px]">
        <div className="p-4">
          <PasswordChangeForm
            variant="temporary"
            onChanged={async () => {
              await refetchMe();
              await navigate({ to: '/' });
            }}
          />
        </div>
      </Card>
    </BarePage>
  );
}
