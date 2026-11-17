import { CELL_MINOR, type DataListColumn } from '../../../ui/data-list/index.ts';
import type { RepoDefinition } from '../../../api/types.ts';
import type { Messages } from '../../../i18n/types.ts';

/** sharedRepoColumns is every column of the shared repository list (list id `shared-repos`). */
export function sharedRepoColumns(t: Messages): DataListColumn<RepoDefinition>[] {
  const s = t.instance.shared;
  return [
    {
      id: 'name',
      label: s.name,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-mono">{r.name}</span>,
    },
    {
      id: 'url',
      label: s.repos.url,
      width: 'minmax(0, 1.3fr)',
      sortKey: 'url',
      sortValue: (r) => r.url,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{r.url}</span>,
    },
    {
      id: 'branch',
      label: s.repos.branch,
      width: '110px',
      sortKey: 'branch',
      sortValue: (r) => r.branch,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono ${CELL_MINOR}`}>{r.branch}</span>,
    },
    {
      id: 'subpath',
      label: s.repos.subpath,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'subpath',
      sortValue: (r) => r.subpath,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{r.subpath || '—'}</span>,
    },
  ];
}
