import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import { Pill, type Tone } from '../../ui/badge.tsx';
import type { BackupEntry } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { formatSize } from './backupFormat.ts';

const STATE_TONE: Record<BackupEntry['state'], Tone> = {
  running: 'accent',
  succeeded: 'ok',
  failed: 'crit',
  abandoned: 'warn',
  damaged: 'crit',
};

/**
 * backupColumns is every column of the backup list (list id `backups`),
 * sorted in the browser. The time is the required column; a backup opens
 * nothing, its actions sit in the row menu. The status column carries the
 * state, the "unverified" mark with its explanation (as text a screen reader
 * gets, not only a title) and a failed attempt's error.
 */
export function backupColumns(t: Messages, f: Format): DataListColumn<BackupEntry>[] {
  const l = t.backups.list;
  return [
    {
      id: 'time',
      label: l.time,
      width: 'minmax(0, 1.3fr)',
      sortKey: 'time',
      sortValue: (r) => r.created_at,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate">{f.dateTime(r.created_at)}</span>,
    },
    {
      id: 'kind',
      label: l.kind,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'kind',
      sortValue: (r) => r.kind,
      defaultVisible: true,
      render: (r) => <span className="block truncate">{r.kind === 'manual' ? l.manual(r.actor_name) : l.scheduled}</span>,
    },
    {
      id: 'version',
      label: l.version,
      width: 'minmax(0, 0.6fr)',
      sortKey: 'version',
      sortValue: (r) => r.pulsix_version,
      defaultVisible: true,
      render: (r) => <span className="block truncate">{r.pulsix_version || '—'}</span>,
    },
    {
      id: 'size',
      label: l.size,
      width: 'minmax(0, 0.7fr)',
      sortKey: 'size',
      sortValue: (r) => r.size_bytes,
      defaultVisible: true,
      render: (r) => <span className="block truncate">{r.size_bytes > 0 ? formatSize(f, r.size_bytes) : '—'}</span>,
    },
    {
      id: 'state',
      label: l.state,
      width: 'minmax(0, 2fr)',
      sortKey: 'state',
      sortValue: (r) => r.state,
      defaultVisible: true,
      render: (r) => (
        <span className="flex flex-col items-start gap-1">
          <Pill tone={STATE_TONE[r.state]}>{l.states[r.state]}</Pill>
          {r.state === 'succeeded' && !r.verified && (
            <span title={l.unverifiedHint}>
              <Pill tone="unknown">{l.unverified}</Pill>
              <span className="sr-only">{l.unverifiedHint}</span>
            </span>
          )}
          {r.error && <span className={`break-words text-destructive ${CELL_MINOR}`}>{r.error}</span>}
        </span>
      ),
    },
  ];
}
