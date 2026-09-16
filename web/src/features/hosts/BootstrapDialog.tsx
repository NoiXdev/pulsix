import { useState, type FormEvent } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useBootstrapHost } from '../../api/hooks/useHostOps.ts';


/**
 * BootstrapDialog installs Pulsix's access key with an operator-provided
 * one-time login (spec §5, backlog 2.7c part 2): the login is used once by
 * the worker and never stored — not in the audit trail, not in a log
 * line, and not kept in the client's cache (the hook's gcTime is 0).
 * Exactly one of password and private key is required; the submit lands on
 * the bootstrap job's run view.
 */
export function BootstrapDialog({ org, hostId, hostName, onClose }: { org: string; hostId: string; hostName: string; onClose: () => void }) {
  const toast = useToast();
  const bootstrap = useBootstrapHost(org, hostId);
  const navigate = useNavigate();
  const [user, setUser] = useState('');
  const [mode, setMode] = useState<'password' | 'private_key'>('password');
  const [password, setPassword] = useState('');
  const [privateKey, setPrivateKey] = useState('');
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const secret = mode === 'password' ? password : privateKey;
    if (secret.trim() === '') {
      setError('exactly one of password and private key is required');
      return;
    }
    try {
      const { job_id } = await bootstrap.mutateAsync({
        user,
        password: mode === 'password' ? password : undefined,
        private_key: mode === 'private_key' ? privateKey : undefined,
      });
      toast.show('Bootstrap started');
      await navigate({ to: '/o/$org/runs/$id', params: { org, id: job_id } });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Starting the bootstrap failed');
    }
  }

  return (
    <Dialog open onClose={onClose} title={`Bootstrap ${hostName}`}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <p className="text-xs text-muted-foreground">
          Installs the public key of this host's ssh_key credential with a login you provide here. The login is{' '}
          <span className="font-semibold text-foreground">used once and never stored</span> — not in the audit trail, not in any log.
        </p>
        <div className="flex flex-col gap-1">
          <Label htmlFor="bootstrap-user">Login user</Label>
          <Input id="bootstrap-user" className="font-mono" value={user} onChange={(e) => setUser(e.target.value)} placeholder="root" autoFocus />
        </div>
        <fieldset className="flex flex-col gap-1">
          <legend className="text-[11px] font-medium text-muted-foreground">One-time login</legend>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name="bootstrap-mode" checked={mode === 'password'} onChange={() => setMode('password')} />
            Password
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name="bootstrap-mode" checked={mode === 'private_key'} onChange={() => setMode('private_key')} />
            Private key
          </label>
          {mode === 'password' ? (
            <Input
              id="bootstrap-password"
              type="password"
              autoComplete="new-password"
              className="font-mono"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              aria-label="One-time password"
            />
          ) : (
            <textarea
              id="bootstrap-private-key"
              aria-label="One-time private key"
              rows={4}
              spellCheck={false}
              className="rounded-md border border-border bg-background p-2 font-mono text-[11px]"
              value={privateKey}
              onChange={(e) => setPrivateKey(e.target.value)}
              placeholder={'-----BEGIN OPENSSH PRIVATE KEY-----'}
            />
          )}
          <span className="text-[11px] text-muted-foreground">
            {mode === 'private_key' ? 'The key is used for this single bootstrap run only.' : 'The password is used for this single bootstrap run only.'}
          </span>
        </fieldset>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={bootstrap.isPending || user.trim() === ''}>
            Bootstrap host
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
