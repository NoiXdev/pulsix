import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { OncallRotation } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

/**
 * rotationColumns is every column the on-call rotation list offers (list id
 * `rotations`), sorted in the browser. A rotation opens nothing (its dialog
 * edits it, and its roster sits under the list), so the name is plain text.
 */
export function rotationColumns(t: Messages, channelNames: (r: OncallRotation) => string): DataListColumn<OncallRotation>[] {
  const r8 = t.notifications.rotations;
  return [
    {
      id: 'rotation',
      label: r8.columns.rotation,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-medium">{r.name}</span>,
    },
    {
      id: 'period',
      label: r8.columns.period,
      width: '90px',
      sortKey: 'period',
      sortValue: (r) => r.period_days,
      defaultVisible: true,
      render: (r) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{r8.periodDays(r.period_days)}</span>,
    },
    {
      id: 'cycle',
      label: r8.columns.cycle,
      width: 'minmax(0, 1.4fr)',
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{channelNames(r)}</span>,
    },
  ];
}
