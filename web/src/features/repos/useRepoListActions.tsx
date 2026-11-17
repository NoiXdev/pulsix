import { useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import type { Credential, Repo } from '../../api/types.ts';
import { useSyncRepo } from '../../api/hooks/useRepos.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useToast } from '../../ui/toastContext.ts';
import { TriggersDialog } from '../triggers/TriggersDialog.tsx';
import { useDeleteRepoCommand } from './useDeleteRepoCommand.ts';
import { RepoDialog } from './RepoDialog.tsx';

type OpenDialog = { kind: 'edit' | 'triggers'; repo: Repo } | { kind: 'new' };

export interface RepoListActionsOptions {
  canWrite: boolean;
  credentials: Credential[];
  canCreateCredential: boolean;
}

/**
 * useRepoListActions is the repositories list's row menu for every row:
 * Sync (a run view opens on the sync's job), Triggers, Edit and Delete (not
 * for a projected repository: the server refuses it). `dialogs` holds the
 * one dialog an entry opened; `openNew` is the page's "Add repository".
 */
export function useRepoListActions(org: string, opts: RepoListActionsOptions): { rowActions: (repo: Repo) => RowAction[]; dialogs: ReactNode; openNew: () => void } {
  const t = useT();
  const navigate = useNavigate();
  const toast = useToast();
  const sync = useSyncRepo(org);
  const remove = useDeleteRepoCommand(org);
  const [syncing, setSyncing] = useState<ReadonlySet<string>>(new Set());
  const [dialog, setDialog] = useState<OpenDialog | null>(null);
  const close = () => setDialog(null);

  async function startSync(repo: Repo) {
    if (syncing.has(repo.id)) return;
    setSyncing((p) => new Set(p).add(repo.id));
    try {
      const { job } = await sync.mutateAsync(repo.id);
      toast.show(t.repos.list.syncStarted(repo.name));
      void navigate({ to: '/o/$org/runs/$id', params: { org, id: job.id } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.repos.list.syncError, 'destructive');
    } finally {
      setSyncing((p) => {
        const next = new Set(p);
        next.delete(repo.id);
        return next;
      });
    }
  }

  function rowActions(repo: Repo): RowAction[] {
    const items: RowAction[] = [];
    if (opts.canWrite) items.push({ id: 'sync', label: t.repos.list.sync, onSelect: () => void startSync(repo), disabled: syncing.has(repo.id) });
    items.push({ id: 'triggers', label: t.repos.list.triggers, onSelect: () => setDialog({ kind: 'triggers', repo }) });
    if (opts.canWrite) items.push({ id: 'edit', label: t.common.edit, onSelect: () => setDialog({ kind: 'edit', repo }) });
    // A projection of an installation-owned definition cannot be deleted
    // here (decision 6 refuses it server-side regardless).
    if (opts.canWrite && !repo.shared_repo_id) {
      items.push({ id: 'delete', label: t.common.delete, destructive: true, onSelect: () => void remove.run(repo), disabled: remove.isPending });
    }
    return items;
  }

  return {
    rowActions,
    openNew: () => setDialog({ kind: 'new' }),
    dialogs: dialog && (
      <>
        {dialog.kind === 'triggers' && (
          <TriggersDialog
            org={org}
            target={{ kind: 'repo_sync', id: dialog.repo.id, name: dialog.repo.name }}
            canWrite={opts.canWrite}
            canRun={opts.canWrite}
            onClose={close}
          />
        )}
        {dialog.kind === 'new' && (
          <RepoDialog org={org} credentials={opts.credentials} canCreateCredential={opts.canCreateCredential} onClose={close} />
        )}
        {dialog.kind === 'edit' && (
          <RepoDialog
            org={org}
            repo={{
              id: dialog.repo.id, name: dialog.repo.name, url: dialog.repo.url, branch: dialog.repo.branch, subpath: dialog.repo.subpath,
              credential_id: dialog.repo.credential_id,
              vault_identities: dialog.repo.vault_identities,
              ssh_host_key: dialog.repo.ssh_host_key, pin_state: dialog.repo.pin_state,
              shared_repo_id: dialog.repo.shared_repo_id,
            }}
            credentials={opts.credentials}
            canCreateCredential={opts.canCreateCredential}
            onClose={close}
          />
        )}
      </>
    ),
  };
}
