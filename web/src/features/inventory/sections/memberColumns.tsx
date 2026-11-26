import { Pill } from '../../../ui/badge.tsx';
import type { DataListColumn } from '../../../ui/data-list/index.ts';
import type { InventoryMember } from '../../../api/types.ts';
import type { Messages } from '../../../i18n/types.ts';
import { AssistantPill } from '../AssistantMark.tsx';

/**
 * memberColumns is every column a group's member list offers (list id
 * `group-members`), sorted in the browser. The host is the required column
 * and its row link opens the host; Open host, Edit values and Remove are the
 * row menu's (useMemberListActions).
 */
export function memberColumns(t: Messages): DataListColumn<InventoryMember>[] {
  const m = t.inventory.members;
  return [
    {
      id: 'host',
      label: m.columns.host,
      width: 'minmax(0, 1fr)',
      sortKey: 'host',
      sortValue: (r) => r.host_name,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-mono">{r.host_name}</span>,
    },
    {
      id: 'values',
      label: m.columns.values,
      width: '200px',
      sortKey: 'values',
      sortValue: (r) => (r.problems ?? []).length,
      defaultVisible: true,
      render: (r) => (
        <span className="flex flex-wrap items-center gap-1.5">
          {r.complete ? <Pill tone="ok">{m.complete}</Pill> : <Pill tone="warn">{m.toFill((r.problems ?? []).length)}</Pill>}
          <AssistantPill keys={r.assistant_keys} />
        </span>
      ),
    },
  ];
}
