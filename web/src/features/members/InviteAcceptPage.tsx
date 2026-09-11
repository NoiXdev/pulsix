import { useState, type ReactNode } from 'react';
import { useNavigate, useParams } from '@tanstack/react-router';
import { PulsixMark } from '../../ui/PulsixMark.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { acceptInvite } from '../../api/hooks/useMembers.ts';
import { useRefetchMe } from '../../api/hooks/useMe.ts';

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
 * POST /invites/accept opens a session only when accepting it just gave
 * the address its first password — a brand-new account, or a
 * never-logged-in row left over from an earlier, never-accepted invite
 * (see tenancy.Service.AcceptInvite's own doc comment). When the address
 * already belonged to an account with its own password, acceptance only
 * adds the membership and sets no cookie, so the follow-up
 * `await refetchMe()` below correctly 401s — that is not a failure of
 * acceptance itself (the membership was created either way), it is how
 * this page tells the two outcomes apart without the API needing to say
 * so explicitly in the response body: a new invitee continues signed in
 * towards second-factor enrolment, and the holder of an existing account
 * is told to sign in with the password that account already has.
 */
export function InviteAcceptPage() {
  const { token } = useParams({ strict: false }) as { token: string };
  const navigate = useNavigate();
  const refetchMe = useRefetchMe();

  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [joinedExistingAccount, setJoinedExistingAccount] = useState(false);

  if (joinedExistingAccount) {
    return (
      <InviteAcceptShell>
        <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5">
          <h1 className="text-lg font-semibold">You already have an account</h1>
          <p className="text-xs text-muted-foreground">
            This address already belongs to an account with its own password. You have been added to the
            organization — sign in with that account&apos;s existing password and second factor to continue.
          </p>
          <Button type="button" variant="primary" onClick={() => void navigate({ to: '/login', search: { step: undefined } })}>
            Go to sign in
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
            setError(err instanceof Error ? err.message : 'failed to accept invitation');
            setBusy(false);
            return;
          }
          try {
            await refetchMe();
            await navigate({ to: '/account/security' });
          } catch {
            setJoinedExistingAccount(true);
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
