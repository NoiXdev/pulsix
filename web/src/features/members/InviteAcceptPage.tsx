import { useState } from 'react';
import { useNavigate, useParams } from '@tanstack/react-router';
import { PulsixMark } from '../../ui/PulsixMark.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { acceptInvite } from '../../api/hooks/useMembers.ts';
import { useRefetchMe } from '../../api/hooks/useMe.ts';

/** InviteAcceptPage — no dedicated mockup exists for this screen (like
 * /login); it follows the same split-brand-panel-free, centered-card
 * language as LoginPage for consistency. */
export function InviteAcceptPage() {
  const { token } = useParams({ strict: false }) as { token: string };
  const navigate = useNavigate();
  const refetchMe = useRefetchMe();

  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4 text-foreground">
      <div className="flex w-full max-w-[380px] flex-col gap-7">
        <div className="flex items-center gap-3">
          <PulsixMark size={32} />
          <span className="font-mono text-2xl font-extrabold tracking-tight">
            pulsi<span className="text-brand">x</span>
          </span>
        </div>
        <form
          className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5"
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setError(null);
            try {
              await acceptInvite({ token, display_name: displayName, password });
              await refetchMe();
              await navigate({ to: '/' });
            } catch (err) {
              setError(err instanceof Error ? err.message : 'failed to accept invitation');
            } finally {
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
      </div>
    </div>
  );
}
