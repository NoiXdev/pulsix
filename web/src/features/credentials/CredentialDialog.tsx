import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useCredential, useDeleteCredential, useRotateCredential, useUpdateCredential } from '../../api/hooks/useCredentials.ts';
import { kindLabel, usageLabel } from './labels.ts';
import { PublicKeyPanel } from './PublicKeyPanel.tsx';
import type { Credential } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';

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
export function CredentialDialog({ org, id, canRotate = false, onClose }: { org: string; id: string; canRotate?: boolean; onClose: () => void }) {
  const t = useT();
  const detail = useCredential(org, id);
  const update = useUpdateCredential(org, id);
  const remove = useDeleteCredential(org);
  const c = detail.data?.credential;

  const guard = useQueryGuard(detail);
  if (guard) {
    return (
      <Dialog open onClose={onClose} title={t.credentials.detail.title}>
        {guard}
      </Dialog>
    );
  }

  if (!c) {
    return (
      <Dialog open onClose={onClose} title={t.credentials.detail.title}>
        <QueryLoading />
      </Dialog>
    );
  }

  return (
    <CredentialForm
      org={org}
      credential={c}
      snippets={detail.data?.snippets}
      update={update}
      remove={remove}
      canRotate={canRotate}
      onClose={onClose}
    />
  );
}

function CredentialForm({
  org,
  credential: c,
  snippets,
  update,
  remove,
  canRotate,
  onClose,
}: {
  org: string;
  credential: Credential;
  snippets?: { authorized_keys_command: string; cloud_init: string; terraform: string };
  update: ReturnType<typeof useUpdateCredential>;
  remove: ReturnType<typeof useDeleteCredential>;
  canRotate: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const rotate = useRotateCredential(org);
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
      toast.show(t.credentials.detail.saved);
    } catch (err) {
      setError(err instanceof Error ? err.message : t.credentials.detail.saveFailed);
    }
  }

  async function rotateKey() {
    // Irreversible: the old private key is gone, and every host and jump host
    // using it loses access until the new public key is installed.
    const warning = t.credentials.detail.confirmGenerate(c.name);
    if (!(await ask({ message: warning, destructive: true }))) return;
    try {
      await update.mutateAsync({ name: c.name, username: c.username || undefined, expires_at: c.expires_at ?? undefined, generate: true });
      update.reset();
      toast.show(t.credentials.detail.generated);
    } catch (err) {
      setError(err instanceof Error ? err.message : t.credentials.detail.generateFailed);
    }
  }

  async function onRotate() {
    // Rotation re-keys the credential and installs the new key on every
    // referencing host and jump host; only ssh_key credentials rotate (the
    // server 422s anything else). A failed rotation leaves the credential
    // untouched.
    const warning = t.credentials.detail.confirmRotate(c.name);
    if (!(await ask({ message: warning, destructive: true }))) return;
    try {
      const { job_count } = await rotate.mutateAsync(c.id);
      rotate.reset();
      toast.show(job_count > 0 ? t.credentials.detail.rotationStarted(c.name, job_count) : t.credentials.detail.rotated(c.name));
    } catch (err) {
      setError(err instanceof Error ? err.message : t.credentials.detail.rotateFailed);
    }
  }

  async function onDelete() {
    if (!(await ask({ message: t.credentials.detail.confirmDelete(c.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(c.id);
      toast.show(t.credentials.detail.deleted(c.name));
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.credentials.detail.deleteFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} size="wide" title={c.name}>
      <form onSubmit={save} className="flex max-h-[70vh] flex-col gap-4 overflow-y-auto">
        <span className="text-xs text-muted-foreground">
          {kindLabel(c, t)} · {usageLabel(c, t)}
          {c.fingerprint ? ` · ${c.fingerprint}` : ''}
        </span>
        {c.public_key && <PublicKeyPanel publicKey={c.public_key} snippets={snippets} />}
        <div className="grid grid-cols-2 gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="cred-edit-name">{t.credentials.detail.name}</Label>
            <Input id="cred-edit-name" className="font-mono" value={name ?? c.name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="cred-edit-username">{t.credentials.detail.username}</Label>
            <Input id="cred-edit-username" className="font-mono" value={username ?? c.username} onChange={(e) => setUsername(e.target.value)} />
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="cred-replace">{isKey ? t.credentials.detail.replaceKey : c.kind === 'ssh_password' ? t.credentials.detail.newPassword : t.credentials.detail.newToken}</Label>
          {isKey ? (
            <textarea id="cred-replace" rows={4} className="rounded-md border border-border bg-background p-2 font-mono text-[11px]" value={replacement} onChange={(e) => setReplacement(e.target.value)} />
          ) : (
            <Input id="cred-replace" type="password" autoComplete="new-password" value={replacement} onChange={(e) => setReplacement(e.target.value)} />
          )}
          <span className="text-[11px] text-muted-foreground">{t.credentials.detail.keepHint}</span>
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex items-center justify-between gap-2">
          <Button type="button" variant="destructive" onClick={() => void onDelete()}>
            {t.common.delete}
          </Button>
          <div className="flex gap-2">
            {isKey && (
              <Button type="button" onClick={() => void rotateKey()}>
                {t.credentials.detail.generateKey}
              </Button>
            )}
            {canRotate && c.kind === 'ssh_key' && (
              <Button type="button" onClick={() => void onRotate()} disabled={rotate.isPending}>
                {t.credentials.detail.rotateKey}
              </Button>
            )}
            <Button type="submit" variant="primary" disabled={update.isPending}>
              {t.common.save}
            </Button>
          </div>
        </div>
      </form>
    </Dialog>
  );
}
