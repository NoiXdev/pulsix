import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { InventoryGroupSummary } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

export interface GroupColumnsContext {
  t: Messages;
  /** A group's jump host as the list shows it ('—' for none). */
  bastionName: (id: string | null | undefined) => string;
  /** How deep a group sits in the tree (0 for a top-level group). */
  depthOf: (id: string) => number;
  /** The groups above a group as the list names them ("all_servers › web";
   * '' for a top-level group). */
  pathOf: (id: string) => string;
  /** A sort or a filter is chosen: the rows are flat, so the indent would
   * name a false parent and the path is spelled out instead. */
  flat: boolean;
}

/**
 * groupColumns is every column the inventory groups list offers (list id
 * `groups`), sorted in the browser. The rows come in tree order (a group
 * under its parent), which is the unsorted order; the name keeps its indent
 * so a group's depth stays visible. Choosing a sort lists the groups flat
 * in that order, and a filter flat in tree order, each naming its path.
 */
export function groupColumns({ t, bastionName, depthOf, pathOf, flat }: GroupColumnsContext): DataListColumn<InventoryGroupSummary>[] {
  const g = t.inventory.groups;
  return [
    {
      id: 'name',
      label: g.columns.group,
      width: 'minmax(0, 1fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => {
        const path = pathOf(r.id);
        return (
          <span className="block truncate font-mono" style={{ paddingLeft: flat ? 0 : depthOf(r.id) * 16 }}>
            {r.name}
            {flat && path && <span className={`font-sans text-muted-foreground ${CELL_MINOR}`}> {g.inParent(path)}</span>}
          </span>
        );
      },
    },
    {
      id: 'members',
      label: g.columns.members,
      width: '90px',
      sortKey: 'members',
      sortValue: (r) => r.members,
      defaultVisible: true,
      render: (r) => <span>{r.members}</span>,
    },
    {
      id: 'fields',
      label: g.columns.fields,
      width: '170px',
      sortKey: 'fields',
      sortValue: (r) => r.group_fields + r.host_fields,
      defaultVisible: true,
      render: (r) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{g.fields(r.group_fields, r.host_fields)}</span>,
    },
    {
      id: 'values',
      label: g.columns.values,
      width: '120px',
      sortKey: 'values',
      sortValue: (r) => r.complete,
      defaultVisible: true,
      render: (r) => (r.complete ? <Pill tone="ok">{g.complete}</Pill> : <Pill tone="warn">{g.incomplete}</Pill>),
    },
    {
      id: 'jump_host',
      label: g.columns.jumpHost,
      width: '160px',
      sortKey: 'jump_host',
      sortValue: (r) => (r.bastion_id ? bastionName(r.bastion_id) : null),
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{bastionName(r.bastion_id)}</span>,
    },
  ];
}
