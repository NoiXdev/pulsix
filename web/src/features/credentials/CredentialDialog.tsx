import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useCredential, useDeleteCredential, useUpdateCredential } from '../../api/hooks/useCredentials.ts';
import { kindLabel, usageLabel } from './labels.ts';
import { PublicKeyPanel } from './PublicKeyPanel.tsx';
import type { Credential } from '../../api/types.ts';

/** CredentialDialog shows one credential: its public key and snippets, its
 * metadata for editing, a way to replace its secret, and delete.
 *
 * The loading/error guard lives here, and the actual form is a separate
 * component (CredentialForm, mirroring HostSshCard/HostSshForm) that takes
 * `credential: Credential` as a required prop — TypeScript's control-flow
 * narrowing of `detail.data?.credential` does not reach into closures
 * defined later in the same function body (save/rotateKey/onDelete would
 * each need a `c!` non-null assertion otherwise), and this split avoids
 * that without disabling any lint rule.
 *
 * A failed detail fetch (403/404/500, ...) goes through `useQueryGuard` —
 * the same single place every other page in the app renders a failed
 * query through (ui/QueryState.tsx's doc comment, "Important 7") — rather
 * than being swallowed by the `!c` check below, which used to render
 * `QueryLoading` forever on an error because `detail.data` never arrives.
 * The guard element is rendered inside this dialog's own title/Close/Escape
 * chrome instead of replacing it, so a failed load can still be dismissed
 * like any other dialog. */
export function CredentialDialog({ org, id, onClose }: { org: string; id: string; onClose: () => void }) {
  const detail = useCredential(org, id);
  const update = useUpdateCredential(org, id);
  const remove = useDeleteCredential(org);
  const c = detail.data?.credential;

  const guard = useQueryGuard(detail);
  if (guard) {
    return (
      <Dialog open onClose={onClose} title="Credential">
        {guard}
      </Dialog>
    );
  }

  if (!c) {
    return (
      <Dialog open onClose={onClose} title="Credential">
        <QueryLoading />
      </Dialog>
    );
  }

  return (
    <CredentialForm
      credential={c}
      snippets={detail.data?.snippets}
      update={update}
      remove={remove}
      onClose={onClose}
    />
  );
}

function CredentialForm({
  credential: c,
  snippets,
  update,
  remove,
  onClose,
}: {
  credential: Credential;
  snippets?: { authorized_keys_command: string; cloud_init: string; terraform: string };
  update: ReturnType<typeof useUpdateCredential>;
  remove: ReturnType<typeof useDeleteCredential>;
  onClose: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState<string | null>(null);
  const [username, setUsername] = useState<string | null>(null);
  const [replacement, setReplacement] = useState('');
  const [error, setError] = useState<string | null>(null);
  const isKey = c.kind === 'ssh_key' || c.kind === 'git_key';

  async function save(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const material = replacement === '' ? {} : isKey ? { private_key: replacement } : c.kind === 'ssh_password' ? { password: replacement } : { token: replacement };
    try {
      await update.mutateAsync({ name: name ?? c.name, username: (username ?? c.username) || undefined, expires_at: c.expires_at ?? undefined, ...material });
      setReplacement('');
      update.reset();
      toast.show('Credential saved');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  async function rotateKey() {
    try {
      await update.mutateAsync({ name: c.name, username: c.username || undefined, expires_at: c.expires_at ?? undefined, generate: true });
      update.reset();
      toast.show('New key generated; install the new public key before hosts use it');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Generating failed');
    }
  }

  async function onDelete() {
    if (!confirm(`Delete credential ${c.name}?`)) return;
    try {
      await remove.mutateAsync(c.id);
      toast.show(`Credential ${c.name} deleted`);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Deleting failed');
    }
  }

  return (
    <Dialog open onClose={onClose} size="wide" title={c.name}>
      <form onSubmit={save} className="flex max-h-[70vh] flex-col gap-4 overflow-y-auto">
        <span className="text-xs text-muted-foreground">
          {kindLabel(c)} · {usageLabel(c)}
          {c.fingerprint ? ` · ${c.fingerprint}` : ''}
        </span>
        {c.public_key && <PublicKeyPanel publicKey={c.public_key} snippets={snippets} />}
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="cred-edit-name">Name</Label>
            <Input id="cred-edit-name" className="font-mono" value={name ?? c.name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="cred-edit-username">Username</Label>
            <Input id="cred-edit-username" className="font-mono" value={username ?? c.username} onChange={(e) => setUsername(e.target.value)} />
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="cred-replace">{isKey ? 'Replace with an imported private key' : c.kind === 'ssh_password' ? 'New password' : 'New token'}</Label>
          {isKey ? (
            <textarea id="cred-replace" rows={4} className="rounded-md border border-border bg-background p-2 font-mono text-[11px]" value={replacement} onChange={(e) => setReplacement(e.target.value)} />
          ) : (
            <Input id="cred-replace" type="password" autoComplete="new-password" value={replacement} onChange={(e) => setReplacement(e.target.value)} />
          )}
          <span className="text-[11px] text-muted-foreground">Leave empty to keep the stored secret.</span>
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex items-center justify-between gap-2">
          <Button type="button" variant="destructive" onClick={() => void onDelete()}>
            Delete
          </Button>
          <div className="flex gap-2">
            {isKey && (
              <Button type="button" onClick={() => void rotateKey()}>
                Generate new key
              </Button>
            )}
            <Button type="submit" variant="primary" disabled={update.isPending}>
              Save
            </Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}
