import { Pill, Tag } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { CheckDefinition, MonitoringGroup } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { Binding } from './CheckCells.tsx';
import { bindingText } from './binding.ts';
import { intervalLabel } from './interval.ts';

export interface CheckColumnsContext {
  org: string;
  t: Messages;
  groups: MonitoringGroup[];
}

/**
 * checkColumns is every column the check definitions list offers (list id
 * `checks`), sorted in the browser. A definition opens nothing (there is no
 * check page), so the key is plain text and the binding keeps its link.
 */
export function checkColumns({ org, t, groups }: CheckColumnsContext): DataListColumn<CheckDefinition>[] {
  const c = t.checks.list.columns;
  return [
    {
      id: 'check',
      label: c.check,
      width: 'minmax(0, 1fr)',
      sortKey: 'name',
      sortValue: (r) => r.key,
      required: true,
      defaultVisible: true,
      render: (r) => (
        <span className="flex min-w-0 items-center gap-2">
          <span className="block truncate font-mono text-foreground">{r.key}</span>
          <Tag>{t.checks.origin[r.origin]}</Tag>
        </span>
      ),
    },
    {
      id: 'plugin',
      label: c.plugin,
      width: '110px',
      sortKey: 'plugin',
      sortValue: (r) => r.plugin,
      defaultVisible: true,
      render: (r) => <span className={`block truncate ${CELL_MINOR}`}>{r.plugin}</span>,
    },
    {
      id: 'binds_to',
      label: c.bindsTo,
      width: 'minmax(0, 1.1fr)',
      sortKey: 'binds_to',
      sortValue: (r) => bindingText(r, groups, t),
      defaultVisible: true,
      render: (r) => <Binding org={org} check={r} groups={groups} />,
    },
    {
      id: 'interval',
      label: c.interval,
      width: '90px',
      sortKey: 'interval',
      sortValue: (r) => r.interval_seconds,
      defaultVisible: true,
      render: (r) => <span>{intervalLabel(r.interval_seconds, t)}</span>,
    },
    {
      id: 'state',
      label: c.state,
      width: '110px',
      sortKey: 'state',
      sortValue: (r) => r.enabled,
      defaultVisible: true,
      render: (r) => (r.enabled ? <Pill tone="ok">{t.checks.enabled}</Pill> : <Pill tone="unknown">{t.checks.disabled}</Pill>),
    },
  ];
}
