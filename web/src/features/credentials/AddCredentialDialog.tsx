import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCreateCredential } from '../../api/hooks/useCredentials.ts';
import { KIND_LABEL } from './labels.ts';
import { PublicKeyPanel } from './PublicKeyPanel.tsx';

export type CredentialKind = 'ssh_key' | 'ssh_password' | 'git_key' | 'git_token' | 'vault_password';

interface Created {
  publicKey: string;
  snippets?: { authorized_keys_command: string; cloud_init: string; terraform: string } | null;
}

export function AddCredentialDialog({
  org, open, onClose, onCreated, initialName, initialKind,
}: {
  org: string;
  open: boolean;
  onClose: () => void;
  /** Called with the new credential's id as soon as it exists on the
   * server — not when the one-time public-key panel below is dismissed.
   * The id is a real reference the moment the object exists; gating it
   * behind "Done" would conflate "can this be picked" with "has the
   * secret been acknowledged", two different concerns this dialog happens
   * to show in sequence for a key-kind credential. */
  onCreated: (id: string) => void;
  /** Prefills the name, from what had been typed into the picker. Same prop shape BastionDialog and ZoneDialog give. */
  initialName?: string;
  /** Prefills the Kind field, for a create row opened from a picker that
   * only accepts certain kinds (RepoDialog's Credential wants
   * git_key/git_token, its Vault password wants vault_password) — without
   * it the dialog's own ssh_key default produces a credential the
   * triggering field's own filter excludes, which then renders as
   * "(unavailable)" in the very picker that just created it (v0.4g Task 7
   * fix round). Read only once, at mount, same as `initialName`. */
  initialKind?: CredentialKind;
}) {
  const toast = useToast();
  const create = useCreateCredential(org);
  const [kind, setKind] = useState<CredentialKind>(initialKind ?? 'ssh_key');
  const [mode, setMode] = useState<'generate' | 'import'>('generate');
  const [name, setName] = useState(initialName ?? '');
  const [username, setUsername] = useState('');
  const [expires, setExpires] = useState('');
  const [privateKey, setPrivateKey] = useState('');
  const [passphrase, setPassphrase] = useState('');
  const [secret, setSecret] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<Created | null>(null);
  const isKey = kind === 'ssh_key' || kind === 'git_key';

  const clearSecrets = () => {
    setPrivateKey('');
    setPassphrase('');
    setSecret('');
  };

  function close() {
    create.reset();
    clearSecrets();
    setName('');
    setUsername('');
    setExpires('');
    setError(null);
    setCreated(null);
    onClose();
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const material = isKey
      ? mode === 'generate'
        ? { generate: true }
        : { private_key: privateKey, passphrase: passphrase || undefined }
      : kind === 'ssh_password' || kind === 'vault_password'
        ? // A vault passphrase is a password. Sending it as a token is
          // refused by the server, and were it not, the worker would
          // write an empty password file and Ansible would fail to
          // decrypt with nothing pointing at the cause.
          { password: secret }
        : { token: secret };
    try {
      const res = await create.mutateAsync({
        name, kind, username: username || undefined, expires_at: expires ? new Date(`${expires}T00:00:00Z`).toISOString() : undefined, ...material,
      });
      clearSecrets();
      create.reset();
      toast.show(`Credential ${res.credential.name} created`);
      onCreated(res.credential.id);
      if (res.credential.public_key) setCreated({ publicKey: res.credential.public_key, snippets: res.snippets });
      else close();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Creating the credential failed');
    }
  }

  if (created) {
    return (
      <Dialog open={open} onClose={close} size="wide" title="Install the public key">
        <div className="flex flex-col gap-4">
          <p className="text-xs text-muted-foreground">The private key stays sealed in Pulsix and is never shown. Install this public key where the credential logs in.</p>
          <PublicKeyPanel publicKey={created.publicKey} snippets={created.snippets} />
          <div className="flex justify-end">
            <Button variant="primary" onClick={close}>
              Done
            </Button>
          </div>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog open={open} onClose={close} title="Add credential">
      <form onSubmit={submit} className="flex flex-col gap-4">
        <div className="flex flex-col gap-1">
          <Label htmlFor="cred-kind">Kind</Label>
          <Select
            id="cred-kind"
            options={Object.entries(KIND_LABEL).map(([k, label]) => ({ value: k, label }))}
            value={kind}
            onChange={(value) => setKind(value as CredentialKind)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="cred-name">Name</Label>
          <Input id="cred-name" className="font-mono" required value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="cred-username">Username</Label>
          <Input id="cred-username" className="font-mono" value={username} onChange={(e) => setUsername(e.target.value)} />
          <span className="text-[11px] text-muted-foreground">
            {kind.startsWith('ssh') ? 'The login user; a host can name another.' : 'Optional, for example oauth2 for GitLab tokens.'}
          </span>
        </div>
        {isKey && (
          <fieldset className="flex flex-col gap-2">
            <legend className="text-[11px] font-medium text-muted-foreground">Key</legend>
            <label className="flex items-center gap-2 text-xs">
              <input type="radio" name="cred-mode" checked={mode === 'generate'} onChange={() => setMode('generate')} /> Generate a new ed25519 key
            </label>
            <label className="flex items-center gap-2 text-xs">
              <input type="radio" name="cred-mode" checked={mode === 'import'} onChange={() => setMode('import')} /> Import a private key
            </label>
            {mode === 'import' && (
              <>
                <Label htmlFor="cred-private-key">Private key</Label>
                <textarea id="cred-private-key" rows={6} required className="rounded-md border border-border bg-background p-2 font-mono text-[11px]" value={privateKey} onChange={(e) => setPrivateKey(e.target.value)} />
                <Label htmlFor="cred-passphrase">Passphrase (if the key has one; used once, never stored)</Label>
                <Input id="cred-passphrase" type="password" autoComplete="off" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} />
              </>
            )}
          </fieldset>
        )}
        {!isKey && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="cred-secret">
              {kind === 'ssh_password' ? 'Password' : kind === 'vault_password' ? 'Vault password' : 'Token'}
            </Label>
            <Input id="cred-secret" type="password" autoComplete="new-password" required value={secret} onChange={(e) => setSecret(e.target.value)} />
          </div>
        )}
        <div className="flex flex-col gap-1">
          <Label htmlFor="cred-expires">Expires (optional)</Label>
          <Input id="cred-expires" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending}>
            Create credential
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
