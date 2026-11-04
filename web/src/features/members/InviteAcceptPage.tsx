import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from '@tanstack/react-router';
import { AuthShell } from '../../ui/auth-shell.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Field } from '../../ui/field.tsx';
import { acceptInvite } from '../../api/hooks/useMembers.ts';
import { useMe, useRefetchMe } from '../../api/hooks/useMe.ts';
import { ApiError } from '../../api/client.ts';
import { useResetSession } from '../../lib/session.ts';
import { MIN_PASSWORD_LENGTH, passwordLength } from '../../lib/password.ts';
import { useT } from '../../i18n/useT.ts';

/** InviteAcceptPage — no dedicated mockup exists for this screen (like
 * /login); it follows the same split-brand-panel-free, centered-card
 * language as LoginPage for consistency.
 *
 * The invitation link alone joins only an address that has no account yet
 * (backlog 7.10, see tenancy.Service.AcceptInvite's own doc comment):
 *
 * - Anonymously the page asks for a name and a password. Accepting creates
 *   the account and opens a session (`signed_in`), and the new invitee
 *   continues towards second-factor enrolment. If the address already has
 *   an account, the server answers 409 without using the invitation up,
 *   and the page asks its holder to sign in and open the link again.
 * - Signed in (MFA-complete), the page offers to join as that account and
 *   posts the token alone; the server checks that the invitation is for
 *   this account's address.
 *
 * Any failure — of acceptance itself, or of loading the new session
 * afterwards — is reported as such, never mistaken for another outcome.
 */
export function InviteAcceptPage() {
  const { token } = useParams({ strict: false }) as { token: string };
  const navigate = useNavigate();
  const t = useT();
  const refetchMe = useRefetchMe();
  const resetSession = useResetSession();
  const me = useMe();
  const qc = useQueryClient();

  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [tooShort, setTooShort] = useState(false);
  const [busy, setBusy] = useState(false);
  const [signInRequired, setSignInRequired] = useState(false);

  if (me.isPending) {
    return (
      <AuthShell>
        <p className="text-xs text-muted-foreground">{t.common.loading}</p>
      </AuthShell>
    );
  }

  if (signInRequired) {
    return (
      <AuthShell>
        <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5">
          <h1 className="text-lg font-semibold">{t.members.accept.alreadyTitle}</h1>
          <p className="text-xs text-muted-foreground">{t.members.accept.alreadyBody}</p>
          <Button type="button" variant="primary" onClick={() => void navigate({ to: '/login', search: { step: undefined } })}>
            {t.members.accept.goToSignIn}
          </Button>
        </div>
      </AuthShell>
    );
  }

  const account = me.data?.mfa_complete ? me.data.user : undefined;
  if (account) {
    return (
      <AuthShell>
        <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5">
          <h1 className="text-lg font-semibold">{t.members.accept.joinTitle}</h1>
          <p className="text-xs text-muted-foreground">
            {t.members.accept.signedInAs} <span className="font-medium text-foreground">{account.email}</span>.{' '}
            {t.members.accept.signedInHint}
          </p>
          {error && (
            <p className="text-xs text-destructive">
              {error} {t.members.accept.wrongAddress}
            </p>
          )}
          <Button
            type="button"
            variant="primary"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              setError(null);
              try {
                await acceptInvite({ token });
              } catch (err) {
                setError(err instanceof Error ? err.message : t.members.accept.acceptFailed);
                setBusy(false);
                return;
              }
              // The account's organization list just changed.
              await qc.invalidateQueries();
              await navigate({ to: '/' });
            }}
          >
            {t.members.accept.join}
          </Button>
        </div>
      </AuthShell>
    );
  }

  return (
    <AuthShell>
      <form
        className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5"
        onSubmit={async (e) => {
          e.preventDefault();
          const short = passwordLength(password) < MIN_PASSWORD_LENGTH;
          setTooShort(short);
          if (short) return;
          setBusy(true);
          setError(null);
          try {
            await acceptInvite({ token, display_name: displayName, password });
          } catch (err) {
            if (err instanceof ApiError && err.status === 409) {
              setSignInRequired(true);
            } else {
              setError(err instanceof Error ? err.message : t.members.accept.acceptFailed);
            }
            setBusy(false);
            return;
          }
          try {
            // A new account's session: start from an empty cache
            // (lib/session.ts) and continue to second-factor enrolment,
            // the only thing it may do until it has one.
            resetSession();
            await refetchMe();
            await navigate({ to: '/account/enrol' });
          } catch (err) {
            setError(
              err instanceof Error
                ? t.members.accept.joinedButSignInFailedWith(err.message)
                : t.members.accept.joinedButSignInFailed,
            );
            setBusy(false);
          }
        }}
      >
        <h1 className="text-lg font-semibold">{t.members.accept.joinTitle}</h1>
        <Field id="name" label={t.members.accept.yourName}>
          <Input id="name" required value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        </Field>
        <Field
          id="password"
          label={t.members.accept.choosePassword}
          hint={t.auth.minLengthHint(MIN_PASSWORD_LENGTH)}
          error={tooShort ? t.validation.minChars(MIN_PASSWORD_LENGTH) : null}
        >
          <Input
            id="password"
            type="password"
            autoComplete="new-password"
            required
            minLength={MIN_PASSWORD_LENGTH}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        {error && <p className="text-xs text-destructive">{error}</p>}
        <Button type="submit" variant="primary" disabled={busy}>
          {t.members.accept.accept}
        </Button>
      </form>
    </AuthShell>
  );
}
