import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { MaintenanceWindow } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import type { Format } from '../../i18n/format.ts';
import { targetLabel, whenLabel } from './time.ts';

/**
 * windowColumns is every column the maintenance windows list offers (list id
 * `maintenance`), sorted in the browser. A window opens nothing (its edit
 * dialog is the row menu's Edit), so the name is plain text with the note
 * beneath it. "Why" is a column of its own: the two reasons do entirely
 * different things (a maintenance window lets schedules skip runs, a silence
 * window mutes alerts and leaves runs alone), and a list that rendered them
 * identically left no way to tell one from the other short of opening each.
 */
export function windowColumns(t: Messages, fmt: Format): DataListColumn<MaintenanceWindow>[] {
  const c = t.maintenance.columns;
  return [
    {
      id: 'window',
      label: c.window,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => (
        <span className="flex min-w-0 flex-col">
          <span className="block truncate font-medium">{r.name}</span>
          {r.note && <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{r.note}</span>}
        </span>
      ),
    },
    {
      id: 'why',
      label: c.why,
      width: '110px',
      sortKey: 'why',
      sortValue: (r) => r.reason,
      defaultVisible: true,
      render: (r) => <Pill tone={r.reason === 'silence' ? 'unknown' : 'maintenance'}>{t.maintenance.reason[r.reason === 'silence' ? 'silence' : 'maintenance']}</Pill>,
    },
    {
      id: 'applies_to',
      label: c.appliesTo,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'applies_to',
      sortValue: (r) => targetLabel(r, t),
      defaultVisible: true,
      render: (r) => <span className={`block truncate ${CELL_MINOR}`}>{targetLabel(r, t)}</span>,
    },
    {
      id: 'when',
      label: c.when,
      width: 'minmax(0, 1.3fr)',
      sortKey: 'when',
      // A one-off window sorts by its start, a recurring one by its wording.
      sortValue: (r) => (r.cron ? whenLabel(r, t, fmt) : r.starts_at),
      defaultVisible: true,
      render: (r) => <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{whenLabel(r, t, fmt)}</span>,
    },
    {
      id: 'status',
      label: c.status,
      width: '120px',
      sortKey: 'status',
      sortValue: (r) => r.active_now,
      defaultVisible: true,
      render: (r) => (r.active_now ? <Pill tone="maintenance">{t.maintenance.activeNow}</Pill> : <span className={`text-muted-foreground ${CELL_MINOR}`}>—</span>),
    },
  ];
}
