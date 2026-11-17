import { Tag } from '../../ui/badge.tsx';
import type { DataListColumn } from '../../ui/data-list/index.ts';
import type { MonitoringGroup } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

/**
 * monitoringGroupColumns is every column the monitoring groups list offers
 * (list id `monitoring-groups`), sorted in the browser. A group opens
 * nothing (there is no group page here), so the name is plain text.
 */
export function monitoringGroupColumns(t: Messages): DataListColumn<MonitoringGroup>[] {
  const c = t.checks.groups.columns;
  return [
    {
      id: 'group',
      label: c.group,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-medium">{r.name}</span>,
    },
    {
      id: 'selector',
      label: c.selector,
      width: 'minmax(0, 1.4fr)',
      sortKey: 'selector',
      sortValue: (r) => r.selector,
      defaultVisible: true,
      render: (r) => <Tag className="truncate">{r.selector}</Tag>,
    },
    {
      id: 'priority',
      label: c.priority,
      width: '90px',
      sortKey: 'priority',
      sortValue: (r) => r.priority,
      defaultVisible: true,
      render: (r) => <span>{r.priority}</span>,
    },
  ];
}
