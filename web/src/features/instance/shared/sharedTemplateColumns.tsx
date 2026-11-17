import { CELL_MINOR, type DataListColumn } from '../../../ui/data-list/index.ts';
import type { TemplateDefinition } from '../../../api/types.ts';
import type { Messages } from '../../../i18n/types.ts';

/** The repository column's text: the repository's name, else the id's head. */
function repoLabel(d: TemplateDefinition, repoName: ReadonlyMap<string, string>): string {
  return repoName.get(d.shared_repo_id) ?? d.shared_repo_id.slice(0, 8);
}

/**
 * sharedTemplateColumns is every column of the shared job template list
 * (list id `shared-templates`). `repoName` maps a shared repository's id to
 * its name, from the repository list's own query; until that loads (or if it
 * fails) the id's head shows instead, a legitimate state, not an error.
 */
export function sharedTemplateColumns(t: Messages, repoName: ReadonlyMap<string, string>): DataListColumn<TemplateDefinition>[] {
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
      id: 'repository',
      label: s.templates.repository,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'repository',
      sortValue: (r) => repoLabel(r, repoName),
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{repoLabel(r, repoName)}</span>,
    },
    {
      id: 'playbook',
      label: s.templates.playbookPath,
      width: 'minmax(0, 1.1fr)',
      sortKey: 'playbook',
      sortValue: (r) => r.playbook_path,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono ${CELL_MINOR}`}>{r.playbook_path}</span>,
    },
    {
      id: 'limit',
      label: s.templates.limit,
      width: '110px',
      sortKey: 'limit',
      sortValue: (r) => r.limit,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{r.limit || '—'}</span>,
    },
  ];
}
