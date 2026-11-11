import { useState, type ChangeEvent, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { identitiesForBody, rowsFromIdentities, vaultIdentityErrors, type VaultIdentityRow } from '../../lib/vaultIdentity.ts';
import { useCreateRepo, useClearRepoKey, useDeleteRepo, useUpdateRepo } from '../../api/hooks/useRepos.ts';
import type { Credential, VaultIdentity } from '../../api/types.ts';
import { AddCredentialDialog } from '../credentials/AddCredentialDialog.tsx';
import { kindName } from '../credentials/labels.ts';
import { useT } from '../../i18n/useT.ts';
import { VaultIdentitiesField } from '../credentials/VaultIdentitiesField.tsx';
import { repoErrorsFromServer, validateRepoInput, type RepoFormValues } from './validate.ts';
import { useConfirm } from '../../ui/confirmContext.ts';

export interface RepoDialogProps {
  org: string;
  /** A repo to edit; absent means create. sshHostKey is the git server
   * key pin the last sync observed (SHA256:…), absent while unpinned. */
  repo?: {
    id: string; name: string; url: string; branch: string; subpath: string; credential_id?: string;
    vault_identities?: VaultIdentity[] | null; ssh_host_key?: string; pin_state?: string;
    /** Set when this repository is a projection of an installation-owned
     * definition (spec v0.2f decisions 1, 2). Its presence, not its value,
     * is what the dialog acts on: name, url, branch and subpath become
     * read-only and the delete action disappears (decision 6 refuses the
     * delete server-side regardless of what a caller sends, so offering
     * the button here would only ever produce a refusal). */
    shared_repo_id?: string;
  };
  credentials: readonly Credential[];
  /** Gates both credential pickers' create rows: credentials.write is
   * admin, stricter than the repos.write that gates this whole dialog
   * (v0.4g Task 7 fix round). */
  canCreateCredential: boolean;
  onClose: () => void;
}

/** Who asked a credential picker to open AddCredentialDialog: the single
 * Credential field, or one row of the Vault identities list (named by its
 * own row key, since there can be any number of those). Only one such
 * request exists at a time — the dialog mount below is a sibling of every
 * picker, not owned by any one of them. */
type AddingCredential = { field: 'credential'; name: string } | { field: 'vault-identity'; rowKey: string; name: string };

type FieldErrors = Partial<Record<keyof RepoFormValues, string>>;

/** RepoDialog creates or edits one git repository. Its validation mirrors
 * the server (repos/validate.ts — the same messages) and a rejected save
 * surfaces the server's detail at the field it names. */
export function RepoDialog({ org, repo, credentials, canCreateCredential, onClose }: RepoDialogProps) {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const create = useCreateRepo(org);
  const update = useUpdateRepo(org, repo?.id ?? '');
  const remove = useDeleteRepo(org);
  const clearKey = useClearRepoKey(org, repo?.id ?? '');
  // Set means this row is a projection of an installation-owned definition
  // (spec v0.2f decision 1): name, url, branch and subpath are the
  // definition's own, refused on write by repos.Service.Update
  // (refuseSharedFieldChange, internal/repos/service.go) regardless of
  // what this dialog sends, so they are disabled rather than merely
  // validated client-side — offering an editable field that always 400s is
  // the same mistake decision 6 calls out for the delete button below.
  const projected = Boolean(repo?.shared_repo_id);
  const [values, setValues] = useState<RepoFormValues>(
    repo ? { name: repo.name, url: repo.url, branch: repo.branch, subpath: repo.subpath } : { name: '', url: '', branch: '', subpath: '' },
  );
  const [credentialId, setCredentialId] = useState(repo?.credential_id ?? '');
  const [vaultRows, setVaultRows] = useState<VaultIdentityRow[]>(() => rowsFromIdentities(repo?.vault_identities));
  const [vaultErrors, setVaultErrors] = useState<Record<string, string>>({});
  const [pinnedKey, setPinnedKey] = useState(repo?.ssh_host_key ?? '');
  const [errors, setErrors] = useState<FieldErrors>({});
  const [formError, setFormError] = useState<string | null>(null);
  // Which field asked for a new credential, and what was typed into it
  // before it did — null means the create dialog is closed. One mount
  // point for the Credential field and every row of the Vault identities
  // list, since only one can be open at a time.
  const [addingCredential, setAddingCredential] = useState<AddingCredential | null>(null);

  function setValue(key: keyof RepoFormValues) {
    return (e: ChangeEvent<HTMLInputElement>) => setValues((v) => ({ ...v, [key]: e.target.value }));
  }

  function field(key: keyof RepoFormValues) {
    const labels = t.repos.dialog.labels;
    return (
      <div className="flex flex-col gap-1">
        <Label htmlFor={`repo-${key}`}>{labels[key]}</Label>
        <Input
          id={`repo-${key}`}
          className="font-mono"
          value={values[key]}
          onChange={setValue(key)}
          placeholder={t.repos.dialog.placeholders[key]}
          aria-invalid={errors[key] ? true : undefined}
          autoFocus={key === 'name'}
          // Every field this helper renders — name, url, branch, subpath —
          // is exactly the set spec decision 2 gives to the installation's
          // definition, so one flag covers all four. Disabled, not
          // hidden/removed: the value still describes the resource, which
          // is the whole reason this dialog shows it in the first place.
          disabled={projected}
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
    const fieldErrors = validateRepoInput(values, t);
    setErrors(fieldErrors);
    const identityErrors = vaultIdentityErrors(vaultRows, t);
    setVaultErrors(identityErrors);
    if (fieldErrors.name || fieldErrors.url || fieldErrors.branch || fieldErrors.subpath) return;
    if (Object.keys(identityErrors).length > 0) return;
    const body = {
      name: values.name, url: values.url, branch: values.branch || 'main',
      subpath: values.subpath, credential_id: credentialId || undefined,
      vault_identities: identitiesForBody(vaultRows),
    };
    try {
      if (repo) await update.mutateAsync(body);
      else await create.mutateAsync(body);
      toast.show(repo ? t.repos.dialog.saved(body.name) : t.repos.dialog.created(body.name));
      onClose();
    } catch (err) {
      const message = err instanceof Error ? err.message : t.repos.dialog.saveFailed;
      const routed = repoErrorsFromServer(message);
      const status = errorStatus(err);
      if (Object.keys(routed).length > 0 && (status === 400 || status === 409)) setErrors(routed);
      else setFormError(message);
    }
  }

  return (
    <Dialog open onClose={onClose} title={repo?.name ?? t.repos.dialog.newTitle}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        {projected && (
          <div className="flex items-center gap-2 rounded-md border border-border bg-background px-2.5 py-1.5">
            <Pill tone="accent">{t.repos.list.managed}</Pill>
            <span className="text-[11px] text-muted-foreground">
              {t.repos.dialog.managedNote}
            </span>
          </div>
        )}
        {field('name')}
        {field('url')}
        {field('branch')}
        {field('subpath')}
        <div className="flex flex-col gap-1">
          <Label htmlFor="repo-credential">{t.repos.dialog.credential}</Label>
          <Select
            id="repo-credential"
            options={credentials
              .filter((c) => c.kind === 'git_key' || c.kind === 'git_token')
              .map((c) => ({ value: c.id, label: c.name, hint: kindName(c.kind, t) }))}
            value={credentialId}
            onChange={setCredentialId}
            placeholder={t.repos.dialog.credentialNone}
            create={
              canCreateCredential
                ? { label: t.repos.dialog.newCredential, onSelect: (text) => setAddingCredential({ field: 'credential', name: text }) }
                : undefined
            }
          />
          <span className="text-[11px] text-muted-foreground">{t.repos.dialog.credentialHint}</span>
        </div>
        <VaultIdentitiesField
          rows={vaultRows}
          onChange={setVaultRows}
          credentials={credentials}
          canCreateCredential={canCreateCredential}
          errors={vaultErrors}
          onRequestCreate={(row, text) => setAddingCredential({ field: 'vault-identity', rowKey: row.key, name: text })}
          help={t.repos.dialog.vaultHelp}
        />
        {repo && (
          <div className="flex items-center justify-between gap-2 rounded-md border border-border px-2 py-1.5">
            <div className="flex min-w-0 flex-col gap-0.5">
              <span className="text-[11px] font-medium">{t.repos.dialog.hostKey}</span>
              {pinnedKey ? (
                <>
                  <span className="truncate font-mono text-[11px]" title={pinnedKey}>
                    {pinnedKey}
                  </span>
                  <span className="text-[11px] text-muted-foreground">{t.repos.dialog.pinned}</span>
                </>
              ) : (
                <span className="text-[11px] text-muted-foreground">{t.repos.dialog.notPinned}</span>
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
                    toast.show(t.repos.dialog.pinCleared);
                  } catch (err) {
                    toast.show(err instanceof Error ? err.message : t.repos.dialog.unpinFailed, 'destructive');
                  }
                }}
              >
                {t.repos.dialog.unpin}
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
          {repo && !projected && (
            <Button
              type="button"
              variant="destructive"
              disabled={remove.isPending}
              onClick={async () => {
                if (!(await ask({ message: t.repos.dialog.confirmDelete(repo.name), confirmLabel: t.common.delete, destructive: true }))) return;
                try {
                  await remove.mutateAsync(repo.id);
                  toast.show(t.repos.dialog.deleted(repo.name));
                  onClose();
                } catch (err) {
                  toast.show(err instanceof Error ? err.message : t.repos.dialog.deleteFailed, 'destructive');
                }
              }}
            >
              {t.common.delete}
            </Button>
          )}
          <div className="flex gap-2">
            <Button type="button" onClick={onClose}>
              {t.common.cancel}
            </Button>
            <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
              {repo ? t.repos.dialog.save : t.repos.dialog.create}
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
            else setVaultRows((rows) => rows.map((r) => (r.key === adding.rowKey ? { ...r, credentialId: id } : r)));
          }}
        />
      )}
    </Dialog>
  );
}
