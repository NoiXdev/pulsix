import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Playbook } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

export interface PlaybookColumnsContext {
  t: Messages;
  /** The name of a repository by id, or undefined when this page does not know it. */
  repoName: (id: string | undefined) => string | undefined;
}

/** A playbook's name: a repo playbook's path, an editor playbook's summary. */
export function playbookLabel(p: Playbook, t: Messages): string {
  return p.source === 'repo' ? (p.path ?? '') : p.summary || t.playbooks.list.unnamed;
}

/**
 * playbookColumns is every column the playbooks list offers (list id
 * `playbooks`), sorted in the browser. The name column (summary or path) is
 * the required one; Edit, for an editor playbook, is the row menu's.
 */
export function playbookColumns({ t, repoName }: PlaybookColumnsContext): DataListColumn<Playbook>[] {
  const c = t.playbooks.list.columns;
  const repoOf = (p: Playbook) => (p.repo_id ? (repoName(p.repo_id) ?? p.repo_id.slice(0, 8)) : '');
  return [
    {
      id: 'name',
      label: c.summaryPath,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'name',
      sortValue: (p) => playbookLabel(p, t),
      required: true,
      defaultVisible: true,
      render: (p) => <span className="block truncate font-mono">{playbookLabel(p, t)}</span>,
    },
    {
      id: 'source',
      label: c.source,
      width: '110px',
      sortKey: 'source',
      sortValue: (p) => p.source,
      defaultVisible: true,
      render: (p) => (
        <span>{p.source === 'repo' ? <Pill tone="accent">{t.playbooks.list.sourceRepo}</Pill> : <Pill tone="unknown">{t.playbooks.list.sourceEditor}</Pill>}</span>
      ),
    },
    {
      id: 'repository',
      label: c.repository,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'repository',
      sortValue: (p) => repoOf(p) || null,
      defaultVisible: true,
      render: (p) => <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{repoOf(p) || '—'}</span>,
    },
    {
      id: 'updated',
      label: c.updated,
      width: '110px',
      sortKey: 'updated',
      sortValue: (p) => p.updated_at,
      defaultVisible: true,
      render: (p) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{p.updated_at.slice(0, 10)}</span>,
    },
  ];
}
