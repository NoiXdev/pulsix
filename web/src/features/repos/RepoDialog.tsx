import { useState, type ChangeEvent, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { identitiesForBody, rowsFromIdentities, vaultIdentityErrors, type VaultIdentityRow } from '../../lib/vaultIdentity.ts';
import { useCreateRepo, useClearRepoKey, useDeleteRepo, useUpdateRepo } from '../../api/hooks/useRepos.ts';
import type { Credential, VaultIdentity } from '../../api/types.ts';
import { AddCredentialDialog } from '../credentials/AddCredentialDialog.tsx';
import { KIND_LABEL } from '../credentials/labels.ts';
import { VaultIdentitiesField } from '../credentials/VaultIdentitiesField.tsx';
import { repoErrorsFromServer, validateRepoInput, type RepoFormValues } from './validate.ts';

export interface RepoDialogProps {
  org: string;
  /** A repo to edit; absent means create. sshHostKey is the git server
   * key pin the last sync observed (SHA256:…), absent while unpinned. */
  repo?: {
    id: string; name: string; url: string; branch: string; subpath: string; credential_id?: string; vault_credential_id?: string;
    vault_identities?: VaultIdentity[] | null; ssh_host_key?: string; pin_state?: string;
  };
  credentials: readonly Credential[];
  /** Gates both credential pickers' create rows: credentials.write is
   * admin, stricter than the repos.write that gates this whole dialog
   * (v0.4g Task 7 fix round). */
  canCreateCredential: boolean;
  onClose: () => void;
}

/** Who asked a credential picker to open AddCredentialDialog: the single
 * Credential field, the legacy single Vault password field, or one row of
 * the Vault identities list (named by its own row key, since there can be
 * any number of those). Only one such request exists at a time — the
 * dialog mount below is a sibling of every picker, not owned by any one of
 * them. */
type AddingCredential = { field: 'credential'; name: string } | { field: 'vault'; name: string } | { field: 'vault-identity'; rowKey: string; name: string };

type FieldErrors = Partial<Record<keyof RepoFormValues, string>>;

/** RepoDialog creates or edits one git repository. Its validation mirrors
 * the server (repos/validate.ts — the same messages) and a rejected save
 * surfaces the server's detail at the field it names. */
export function RepoDialog({ org, repo, credentials, canCreateCredential, onClose }: RepoDialogProps) {
  const toast = useToast();
  const create = useCreateRepo(org);
  const update = useUpdateRepo(org, repo?.id ?? '');
  const remove = useDeleteRepo(org);
  const clearKey = useClearRepoKey(org, repo?.id ?? '');
  const [values, setValues] = useState<RepoFormValues>(
    repo ? { name: repo.name, url: repo.url, branch: repo.branch, subpath: repo.subpath } : { name: '', url: '', branch: '', subpath: '' },
  );
  const [credentialId, setCredentialId] = useState(repo?.credential_id ?? '');
  const [vaultCredentialId, setVaultCredentialId] = useState(repo?.vault_credential_id ?? '');
  const [vaultRows, setVaultRows] = useState<VaultIdentityRow[]>(() => rowsFromIdentities(repo?.vault_identities));
  const [vaultErrors, setVaultErrors] = useState<Record<string, string>>({});
  const [pinnedKey, setPinnedKey] = useState(repo?.ssh_host_key ?? '');
  const [errors, setErrors] = useState<FieldErrors>({});
  const [formError, setFormError] = useState<string | null>(null);
  // Which field asked for a new credential, and what was typed into it
  // before it did — null means the create dialog is closed. One mount
  // point for the Credential field, the legacy Vault password field and
  // every row of the Vault identities list, since only one can be open at
  // a time.
  const [addingCredential, setAddingCredential] = useState<AddingCredential | null>(null);

  function setValue(key: keyof RepoFormValues) {
    return (e: ChangeEvent<HTMLInputElement>) => setValues((v) => ({ ...v, [key]: e.target.value }));
  }

  function field(key: keyof RepoFormValues, placeholder: string) {
    const labels: Record<keyof RepoFormValues, string> = { name: 'Name', url: 'URL', branch: 'Branch', subpath: 'Subdirectory' };
    return (
      <div className="flex flex-col gap-1">
        <Label htmlFor={`repo-${key}`}>{labels[key]}</Label>
        <Input
          id={`repo-${key}`}
          className="font-mono"
          value={values[key]}
          onChange={setValue(key)}
          placeholder={placeholder}
          aria-invalid={errors[key] ? true : undefined}
          autoFocus={key === 'name'}
        />
        {errors[key] && (
          <span role="alert" className="text-[11px] text-destructive">
            {errors[key]}
          </span>
        )}
      </div>
    );
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFormError(null);
    const fieldErrors = validateRepoInput(values);
    setErrors(fieldErrors);
    const identityErrors = vaultIdentityErrors(vaultRows);
    setVaultErrors(identityErrors);
    if (fieldErrors.name || fieldErrors.url || fieldErrors.branch || fieldErrors.subpath) return;
    if (Object.keys(identityErrors).length > 0) return;
    const body = {
      name: values.name, url: values.url, branch: values.branch || 'main',
      subpath: values.subpath, credential_id: credentialId || undefined,
      vault_credential_id: vaultCredentialId || undefined,
      vault_identities: identitiesForBody(vaultRows),
    };
    try {
      if (repo) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(repo ? `Repository ${body.name} saved` : `Repository ${body.name} created`);
      onClose();
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Saving failed';
      const routed = repoErrorsFromServer(message);
      const status = errorStatus(err);
      if (Object.keys(routed).length > 0 && (status === 400 || status === 409)) setErrors(routed);
      else setFormError(message);
    }
  }

  return (
    <Dialog open onClose={onClose} title={repo?.name ?? 'New repository'}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        {field('name', 'infra')}
        {field('url', 'https://git.example/acme/infra.git')}
        {field('branch', 'main')}
        {field('subpath', 'ansible/ (optional)')}
        <div className="flex flex-col gap-1">
          <Label htmlFor="repo-credential">Credential</Label>
          <Select
            id="repo-credential"
            options={credentials
              .filter((c) => c.kind === 'git_key' || c.kind === 'git_token')
              .map((c) => ({ value: c.id, label: c.name, hint: KIND_LABEL[c.kind] ?? c.kind }))}
            value={credentialId}
            onChange={setCredentialId}
            placeholder="— none —"
            create={
              canCreateCredential
                ? { label: 'New credential…', onSelect: (text) => setAddingCredential({ field: 'credential', name: text }) }
                : undefined
            }
          />
          <span className="text-[11px] text-muted-foreground">Deploy key or token the checkout authenticates with.</span>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="repo-vault-credential">Vault password</Label>
          {/* No hint here: every row is a vault_password credential (the
              server refuses any other kind for this field), so a kind hint
              would only ever repeat "Ansible Vault password" — decoration,
              not disambiguation (Move 2). */}
          <Select
            id="repo-vault-credential"
            options={credentials.filter((c) => c.kind === 'vault_password').map((c) => ({ value: c.id, label: c.name }))}
            value={vaultCredentialId}
            onChange={setVaultCredentialId}
            placeholder="— none —"
            create={
              canCreateCredential
                ? { label: 'New credential…', onSelect: (text) => setAddingCredential({ field: 'vault', name: text }) }
                : undefined
            }
          />
          {/* Two secrets, two fields: one reaches the repository, the
              other its contents. Only vault_password credentials are
              offered — the server refuses anything else here, and a
              deploy key accepted as a vault password would fail far
              from its cause, as Ansible's own "decryption failed". */}
          <span className="text-[11px] text-muted-foreground">
            Passphrase Ansible needs for this repository&apos;s encrypted files.
          </span>
        </div>
        <VaultIdentitiesField
          rows={vaultRows}
          onChange={setVaultRows}
          credentials={credentials}
          canCreateCredential={canCreateCredential}
          errors={vaultErrors}
          onRequestCreate={(row, text) => setAddingCredential({ field: 'vault-identity', rowKey: row.key, name: text })}
          help="Additional labelled vault passwords, for encrypted files that use more than one. Leave empty to rely only on the Vault password above."
        />
        {repo && (
          <div className="flex items-center justify-between gap-2 rounded-md border border-border px-2 py-1.5">
            <div className="flex min-w-0 flex-col gap-0.5">
              <span className="text-[11px] font-medium">Git server key</span>
              {pinnedKey ? (
                <>
                  <span className="truncate font-mono text-[11px]" title={pinnedKey}>
                    {pinnedKey}
                  </span>
                  <span className="text-[11px] text-muted-foreground">pinned on first sync — a changed key blocks every fetch</span>
                </>
              ) : (
                <span className="text-[11px] text-muted-foreground">not pinned yet — the first successful sync pins it</span>
              )}
            </div>
            {pinnedKey && (
              <Button
                type="button"
                variant="ghost"
                disabled={clearKey.isPending}
                onClick={async () => {
                  try {
                    await clearKey.mutateAsync();
                    setPinnedKey('');
                    toast.show('Key pin cleared — the next sync re-pins the server');
                  } catch (err) {
                    toast.show(err instanceof Error ? err.message : 'Unpinning failed', 'destructive');
                  }
                }}
              >
                Unpin key
              </Button>
            )}
          </div>
        )}
        {formError && (
          <p role="alert" className="text-xs text-destructive">
            {formError}
          </p>
        )}
        <div className="flex items-center justify-between gap-2">
          {repo && (
            <Button
              type="button"
              variant="destructive"
              disabled={remove.isPending}
              onClick={async () => {
                if (!confirm(`Delete repository ${repo.name}?`)) return;
                try {
                  await remove.mutateAsync(repo.id);
                  toast.show(`Repository ${repo.name} deleted`);
                  onClose();
                } catch (err) {
                  toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
                }
              }}
            >
              Delete
            </Button>
          )}
          <div className="flex gap-2">
            <Button type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
              {repo ? 'Save repository' : 'Create repository'}
            </Button>
          </div>
        </div>
      </form>
      {/* Sibling of the <form> above, not inside it — see HostSshCard.tsx's
          own comment on why a nested <dialog> here would double-submit. */}
      {addingCredential && (
        <AddCredentialDialog
          org={org}
          open
          initialName={addingCredential.name}
          // The Credential field filters to git_key/git_token, Vault
          // password and every Vault identities row to vault_password —
          // none of those match the dialog's own ssh_key default, so
          // without this the new credential would not appear as an option
          // in the very field that created it.
          initialKind={addingCredential.field === 'credential' ? 'git_key' : 'vault_password'}
          onClose={() => setAddingCredential(null)}
          onCreated={(id) => {
            // Narrowed into a local const: TS forgets a discriminated
            // union narrowed from component state the moment it is read
            // inside a nested closure (the `rows.map` callback below), even
            // though `addingCredential` cannot change mid-callback.
            const adding = addingCredential;
            if (adding.field === 'credential') setCredentialId(id);
            else if (adding.field === 'vault') setVaultCredentialId(id);
            else setVaultRows((rows) => rows.map((r) => (r.key === adding.rowKey ? { ...r, credentialId: id } : r)));
          }}
        />
      )}
    </Dialog>
  );
}
