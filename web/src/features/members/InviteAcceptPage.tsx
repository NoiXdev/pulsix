import { useState, type ReactNode } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate, useParams } from '@tanstack/react-router';
import { PulsixMark } from '../../ui/PulsixMark.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { acceptInvite } from '../../api/hooks/useMembers.ts';
import { useMe, useRefetchMe } from '../../api/hooks/useMe.ts';
import { ApiError } from '../../api/client.ts';
import { useResetSession } from '../../lib/session.ts';

function InviteAcceptShell({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4 text-foreground">
      <div className="flex w-full max-w-[380px] flex-col gap-7">
        <div className="flex items-center gap-3">
          <PulsixMark size={32} />
          <span className="font-mono text-2xl font-extrabold tracking-tight">
            pulsi<span className="text-brand">x</span>
          </span>
        </div>
        {children}
      </div>
    </div>
  );
}

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
  const refetchMe = useRefetchMe();
  const resetSession = useResetSession();
  const me = useMe();
  const qc = useQueryClient();

  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [signInRequired, setSignInRequired] = useState(false);

  if (me.isPending) {
    return (
      <InviteAcceptShell>
        <p className="text-xs text-muted-foreground">Loading…</p>
      </InviteAcceptShell>
    );
  }

  if (signInRequired) {
    return (
      <InviteAcceptShell>
        <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5">
          <h1 className="text-lg font-semibold">You already have an account</h1>
          <p className="text-xs text-muted-foreground">
            This address already belongs to an account. Sign in to it with its own password and second factor, then
            open this invitation link again to join the organization.
          </p>
          <Button type="button" variant="primary" onClick={() => void navigate({ to: '/login', search: { step: undefined } })}>
            Go to sign in
          </Button>
        </div>
      </InviteAcceptShell>
    );
  }

  const account = me.data?.mfa_complete ? me.data.user : undefined;
  if (account) {
    return (
      <InviteAcceptShell>
        <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5">
          <h1 className="text-lg font-semibold">Join the organization</h1>
          <p className="text-xs text-muted-foreground">
            You are signed in as <span className="font-medium text-foreground">{account.email}</span>. The invitation
            must be for this address.
          </p>
          {error && (
            <p className="text-xs text-destructive">
              {error} If the invitation is for a different address, sign out and open this invitation link again.
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
                setError(err instanceof Error ? err.message : 'failed to accept invitation');
                setBusy(false);
                return;
              }
              // The account's organization list just changed.
              await qc.invalidateQueries();
              await navigate({ to: '/' });
            }}
          >
            Join organization
          </Button>
        </div>
      </InviteAcceptShell>
    );
  }

  return (
    <InviteAcceptShell>
      <form
        className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError(null);
          try {
            await acceptInvite({ token, display_name: displayName, password });
          } catch (err) {
            if (err instanceof ApiError && err.status === 409) {
              setSignInRequired(true);
            } else {
              setError(err instanceof Error ? err.message : 'failed to accept invitation');
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
                ? `You joined the organization, but signing you in failed: ${err.message}. Try signing in.`
                : 'You joined the organization, but signing you in failed. Try signing in.',
            );
            setBusy(false);
          }
        }}
      >
        <h1 className="text-lg font-semibold">Join the organization</h1>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="name">Your name</Label>
          <Input id="name" required value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="password">Choose a password</Label>
          <Input
            id="password"
            type="password"
            required
            minLength={8}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>
        {error && <p className="text-xs text-destructive">{error}</p>}
        <Button type="submit" variant="primary" disabled={busy}>
          Accept invitation
        </Button>
      </form>
    </InviteAcceptShell>
  );
}
