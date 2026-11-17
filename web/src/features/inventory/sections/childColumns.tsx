import { CELL_MINOR, type DataListColumn } from '../../../ui/data-list/index.ts';
import type { InventoryGroupSummary } from '../../../api/types.ts';
import type { Messages } from '../../../i18n/types.ts';

/** childColumns is every column a group's child list offers (list id
 * `group-children`), sorted in the browser. */
export function childColumns(t: Messages): DataListColumn<InventoryGroupSummary>[] {
  const c = t.inventory.children;
  return [
    {
      id: 'name',
      label: c.columns.group,
      width: 'minmax(0, 1fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-mono">{r.name}</span>,
    },
    {
      id: 'members',
      label: c.columns.members,
      width: '120px',
      sortKey: 'members',
      sortValue: (r) => r.members,
      defaultVisible: true,
      render: (r) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{c.members(r.members)}</span>,
    },
  ];
}
