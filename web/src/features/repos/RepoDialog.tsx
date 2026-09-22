import { useState, type ChangeEvent, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { useCreateRepo, useClearRepoKey, useDeleteRepo, useUpdateRepo } from '../../api/hooks/useRepos.ts';
import type { Credential } from '../../api/types.ts';
import { repoErrorsFromServer, validateRepoInput, type RepoFormValues } from './validate.ts';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

export interface RepoDialogProps {
  org: string;
  /** A repo to edit; absent means create. sshHostKey is the git server
   * key pin the last sync observed (SHA256:…), absent while unpinned. */
  repo?: { id: string; name: string; url: string; branch: string; subpath: string; credential_id?: string; ssh_host_key?: string; pin_state?: string };
  credentials: readonly Credential[];
  onClose: () => void;
}

type FieldErrors = Partial<Record<keyof RepoFormValues, string>>;

/** RepoDialog creates or edits one git repository. Its validation mirrors
 * the server (repos/validate.ts — the same messages) and a rejected save
 * surfaces the server's detail at the field it names. */
export function RepoDialog({ org, repo, credentials, onClose }: RepoDialogProps) {
  const toast = useToast();
  const create = useCreateRepo(org);
  const update = useUpdateRepo(org, repo?.id ?? '');
  const remove = useDeleteRepo(org);
  const clearKey = useClearRepoKey(org, repo?.id ?? '');
  const [values, setValues] = useState<RepoFormValues>(
    repo ? { name: repo.name, url: repo.url, branch: repo.branch, subpath: repo.subpath } : { name: '', url: '', branch: '', subpath: '' },
  );
  const [credentialId, setCredentialId] = useState(repo?.credential_id ?? '');
  const [pinnedKey, setPinnedKey] = useState(repo?.ssh_host_key ?? '');
  const [errors, setErrors] = useState<FieldErrors>({});
  const [formError, setFormError] = useState<string | null>(null);

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
    if (fieldErrors.name || fieldErrors.url || fieldErrors.branch || fieldErrors.subpath) return;
    const body = {
      name: values.name, url: values.url, branch: values.branch || 'main',
      subpath: values.subpath, credential_id: credentialId || undefined,
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
          <select id="repo-credential" className={SELECT} value={credentialId} onChange={(e) => setCredentialId(e.target.value)}>
            <option value="">— none —</option>
            {credentials
              .filter((c) => c.kind === 'git_key' || c.kind === 'git_token')
              .map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
          </select>
          <span className="text-[11px] text-muted-foreground">Deploy key or token the checkout authenticates with.</span>
        </div>
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
    </Dialog>
  );
}
