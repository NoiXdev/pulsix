import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Repo } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { SyncState } from './RepoCells.tsx';

export interface RepoColumnsContext {
  t: Messages;
  /** A repository's credential as the list shows it ('—' for none). */
  credentialName: (id: string | undefined) => string;
}

/**
 * repoColumns is every column the repositories list offers (list id
 * `repos`), sorted in the browser: the whole registry is one request. The
 * name column carries the installation-managed pill (a projection of an
 * installation-owned definition: RepoDialog disables its fields, so the row
 * says why before the dialog opens). Sync, triggers, edit and delete are the
 * row menu's (useRepoListActions).
 */
export function repoColumns({ t, credentialName }: RepoColumnsContext): DataListColumn<Repo>[] {
  const c = t.repos.list.columns;
  return [
    {
      id: 'name',
      label: c.name,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => (
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="truncate font-mono">{r.name}</span>
          {r.shared_repo_id && <Pill tone="accent">{t.repos.list.managed}</Pill>}
        </span>
      ),
    },
    {
      id: 'url',
      label: c.url,
      width: 'minmax(0, 1.3fr)',
      sortKey: 'url',
      sortValue: (r) => r.url,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{r.url}</span>,
    },
    {
      id: 'branch',
      label: c.branch,
      width: '110px',
      sortKey: 'branch',
      sortValue: (r) => r.branch,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-mono">{r.branch}</span>,
    },
    {
      id: 'subpath',
      label: c.subpath,
      width: '110px',
      sortKey: 'subpath',
      sortValue: (r) => r.subpath,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{r.subpath || '—'}</span>,
    },
    {
      id: 'credential',
      label: c.credential,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'credential',
      sortValue: (r) => credentialName(r.credential_id),
      defaultVisible: true,
      render: (r) => <span className="block truncate">{credentialName(r.credential_id)}</span>,
    },
    {
      id: 'last_sync',
      label: c.lastSync,
      width: '150px',
      sortKey: 'last_sync',
      sortValue: (r) => r.last_sync ?? null,
      defaultVisible: true,
      render: (r) => <SyncState repo={r} />,
    },
  ];
}
