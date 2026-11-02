import { useState } from 'react';
import { useNavigate, useParams } from '@tanstack/react-router';
import { GitBranch, RefreshCw, Webhook } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useRepos, useSyncRepo } from '../../api/hooks/useRepos.ts';
import { may } from '../../lib/roles.ts';
import { RepoDialog } from './RepoDialog.tsx';
import { TriggersDialog } from '../triggers/TriggersDialog.tsx';
import type { Repo } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';

const COLUMNS = 'minmax(0, 0.7fr) minmax(0, 1.3fr) 110px 110px minmax(0, 0.9fr) 150px 110px';

/** ReposPage is the git repository registry (spec §7, backlog 2.8): the
 * checkouts playbooks come from, each with its credential, branch, subpath
 * and the last sync's outcome. Writes are operator actions; a viewer gets
 * the read-only list. */
export function ReposPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const orgInfo = useOrg(org);
  const repos = useRepos(org);
  const canWrite = may(orgInfo.data?.org.role, 'repos.write');
  const canReadCredentials = may(orgInfo.data?.org.role, 'credentials.read');
  // Gates RepoDialog's two credential pickers' own create rows:
  // credentials.write is admin, stricter than repos.write (v0.4g Task 7
  // fix round).
  const canCreateCredential = may(orgInfo.data?.org.role, 'credentials.write');
  const credentials = useCredentials(org, { enabled: !!orgInfo.data && canReadCredentials });

  const [dialog, setDialog] = useState<'new' | Repo | null>(null);
  const [triggers, setTriggers] = useState<Repo | null>(null);

  const guard = useQueryGuard(repos);
  if (guard) return guard;
  if (!repos.data) return <QueryLoading />;
  const list = repos.data.repos ?? [];
  const names = credentials.data?.credentials ?? [];

  function syncStarted(_name: string, jobId: string) {
    void navigate({ to: '/o/$org/runs/$id', params: { org, id: jobId } });
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.repos.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.repos.list.summary(list.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setDialog('new')}>
            <GitBranch size={15} /> {t.repos.list.add}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.repos.list.columns.name}</span>
          <span>{t.repos.list.columns.url}</span>
          <span>{t.repos.list.columns.branch}</span>
          <span>{t.repos.list.columns.subpath}</span>
          <span>{t.repos.list.columns.credential}</span>
          <span>{t.repos.list.columns.lastSync}</span>
          <span>{t.repos.list.columns.actions}</span>
        </GridHeader>
        {list.map((r) => (
          <GridRow key={r.id} columns={COLUMNS}>
            <span className="flex min-w-0 items-center gap-1.5">
              {canWrite ? (
                <button type="button" className="truncate text-left font-mono text-[13px] hover:text-primary" onClick={() => setDialog(r)}>
                  {r.name}
                </button>
              ) : (
                <span className="truncate font-mono text-[13px]">{r.name}</span>
              )}
              {/* r.shared_repo_id marks a projection of an installation-owned
                  definition (spec v0.2f decision 1) — RepoDialog disables its
                  name/url/branch/subpath and drops its delete action, so the
                  row needs to say why before the dialog ever opens. */}
              {r.shared_repo_id && <Pill tone="accent">{t.repos.list.managed}</Pill>}
            </span>
            <span className="truncate font-mono text-xs text-muted-foreground">{r.url}</span>
            <span className="truncate font-mono text-xs">{r.branch}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{r.subpath || '—'}</span>
            <span className="truncate text-xs">{credentialName(names, r.credential_id, t)}</span>
            <SyncState repo={r} />
            <span className="flex gap-1">
              <Button type="button" variant="ghost" aria-label={t.repos.list.triggersLabel(r.name)} onClick={() => setTriggers(r)}>
                <Webhook size={13} />
              </Button>
              {canWrite && <SyncButton org={org} repo={r} onStarted={syncStarted} />}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.repos.list.empty}</p>}
      </Card>
      {triggers && (
        <TriggersDialog
          org={org}
          target={{ kind: 'repo_sync', id: triggers.id, name: triggers.name }}
          canWrite={canWrite}
          canRun={canWrite}
          onClose={() => setTriggers(null)}
        />
      )}
      {dialog === 'new' ? (
        <RepoDialog org={org} credentials={names} canCreateCredential={canCreateCredential} onClose={() => setDialog(null)} />
      ) : dialog ? (
        <RepoDialog
          org={org}
          repo={{
            id: dialog.id, name: dialog.name, url: dialog.url, branch: dialog.branch, subpath: dialog.subpath,
            credential_id: dialog.credential_id,
            vault_identities: dialog.vault_identities,
            ssh_host_key: dialog.ssh_host_key, pin_state: dialog.pin_state,
            shared_repo_id: dialog.shared_repo_id,
          }}
          credentials={names}
          canCreateCredential={canCreateCredential}
          onClose={() => setDialog(null)}
        />
      ) : null}
    </>
  );
}

function credentialName(credentials: readonly { id: string; name: string; kind: string }[], id: string | undefined, t: Messages): string {
  if (!id) return '—';
  const c = credentials.find((x) => x.id === id);
  if (!c) return id.slice(0, 8);
  return c.kind === 'git_key' ? t.repos.list.deployKey(c.name) : c.name;
}

/** SyncState is the last sync outcome: crit when the checkout failed (the
 * error rides as the title), ok when it succeeded, warn while it never
 * ran. */
function SyncState({ repo: r }: { repo: Repo }) {
  const t = useT();
  if (r.last_sync_error) {
    return (
      <span title={r.last_sync_error}>
        <Pill tone="crit">{t.repos.list.syncFailed}</Pill>
      </span>
    );
  }
  if (r.last_sync) return <Pill tone="ok">{t.repos.list.synced(r.last_sync.slice(0, 10))}</Pill>;
  return <Pill tone="warn">{t.repos.list.neverSynced}</Pill>;
}

/** SyncButton starts a repo sync: the 202's job id is where the sync's own
 * progress lives, so the click lands on the run view. */
function SyncButton({ org, repo: r, onStarted }: { org: string; repo: Repo; onStarted: (name: string, jobId: string) => void }) {
  const t = useT();
  const toast = useToast();
  const sync = useSyncRepo(org);
  return (
    <Button
      type="button"
      aria-label={t.repos.list.syncLabel(r.name)}
      disabled={sync.isPending}
      onClick={async () => {
        try {
          const { job } = await sync.mutateAsync(r.id);
          toast.show(t.repos.list.syncStarted(r.name));
          onStarted(r.name, job.id);
        } catch (err) {
          toast.show(err instanceof Error ? err.message : t.repos.list.syncError, 'destructive');
        }
      }}
    >
      <RefreshCw size={13} /> {t.repos.list.sync}
    </Button>
  );
}
