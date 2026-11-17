import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { WorkerEnrollment } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import type { useFormat } from '../../i18n/format.ts';

/**
 * enrollmentColumns is every column the pending enrollment list offers
 * (list id `worker-enrollments`, a second list on the Workers page whose
 * sort and page stay in component state). Withdraw is the row menu's.
 */
export function enrollmentColumns(t: Messages, fmt: ReturnType<typeof useFormat>): DataListColumn<WorkerEnrollment>[] {
  const c = t.workers.columns;
  return [
    {
      id: 'enrollment',
      label: c.pendingEnrollment,
      width: 'minmax(0, 1fr)',
      sortKey: 'enrollment',
      sortValue: (e) => e.name,
      required: true,
      defaultVisible: true,
      render: (e) => <span className="block truncate font-mono">{e.name}</span>,
    },
    {
      id: 'expires',
      label: c.expires,
      width: '160px',
      sortKey: 'expires',
      sortValue: (e) => e.expires_at,
      defaultVisible: true,
      render: (e) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.time(e.expires_at)}</span>,
    },
  ];
}
