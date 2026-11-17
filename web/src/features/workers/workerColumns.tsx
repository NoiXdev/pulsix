import { Link } from '@tanstack/react-router';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Worker } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { VersionSkewBadge } from './VersionSkewBadge.tsx';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from './expiry.ts';

const STATUS_TONE: Record<string, Tone> = { online: 'ok', offline: 'warn', revoked: 'unknown' };

export interface WorkerColumnsContext {
  t: Messages;
  org: string;
  /** The moment ages and expiries count from. */
  now: Date;
}

/**
 * workerColumns is every column the workers list offers (list id `workers`),
 * sorted in the browser. A worker opens nothing (there is no worker page),
 * so the name has no row link and the current-job link keeps its own
 * column. Revoke and Delete are the row menu's (useWorkerListActions).
 */
export function workerColumns({ t, org, now }: WorkerColumnsContext): DataListColumn<Worker>[] {
  const c = t.workers.columns;
  const statusLabel = (w: Worker) => (w.status in t.workers.status ? t.workers.status[w.status as keyof typeof t.workers.status] : w.status);
  return [
    {
      id: 'worker',
      label: c.worker,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'worker',
      sortValue: (w) => w.name,
      required: true,
      defaultVisible: true,
      render: (w) => <span className="block truncate font-mono">{w.name}</span>,
    },
    {
      id: 'zone',
      label: c.zone,
      width: 'minmax(0, 0.6fr)',
      sortKey: 'zone',
      sortValue: (w) => w.zone_slug,
      defaultVisible: true,
      render: (w) => <span className="block truncate font-mono">{w.zone_slug}</span>,
    },
    {
      id: 'status',
      label: c.status,
      width: '100px',
      sortKey: 'status',
      sortValue: (w) => w.status,
      defaultVisible: true,
      render: (w) => <Pill tone={STATUS_TONE[w.status] ?? 'unknown'}>{statusLabel(w)}</Pill>,
    },
    {
      id: 'last_seen',
      label: c.lastSeen,
      width: '110px',
      sortKey: 'last_seen',
      sortValue: (w) => w.last_seen ?? null,
      defaultVisible: true,
      render: (w) => (
        <span className={`${CELL_MINOR} ${w.status === 'offline' ? 'text-[var(--px-warn)]' : 'text-muted-foreground'}`}>{seenLabel(w.last_seen, now, t)}</span>
      ),
    },
    {
      id: 'version',
      label: c.version,
      width: 'minmax(0, 1fr)',
      sortKey: 'version',
      sortValue: (w) => w.version || null,
      defaultVisible: true,
      render: (w) => (
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate font-mono">{w.version || '—'}</span>
          {/* A revoked worker never connects again: nothing to update. */}
          {w.status !== 'revoked' && <VersionSkewBadge status={w.version_status} />}
        </span>
      ),
    },
    {
      id: 'certificate',
      label: c.certificate,
      width: '110px',
      sortKey: 'certificate',
      sortValue: (w) => (w.status === 'revoked' ? null : (w.cert_expires_at ?? null)),
      defaultVisible: true,
      render: (w) => {
        const expiring = !!w.cert_expires_at && daysLeft(w.cert_expires_at, now) < EXPIRY_WARN_DAYS;
        return (
          <span className={`${CELL_MINOR} ${expiring ? 'font-semibold text-[var(--px-warn)]' : 'text-muted-foreground'}`} data-expiring={expiring || undefined}>
            {w.status === 'revoked' ? '—' : expiryLabel(w.cert_expires_at, now, t)}
          </span>
        );
      },
    },
    {
      id: 'current_job',
      label: c.currentJob,
      width: 'minmax(0, 0.7fr)',
      sortKey: 'current_job',
      sortValue: (w) => w.current_job_id ?? null,
      defaultVisible: true,
      render: (w) =>
        w.current_job_id ? (
          <Link to="/o/$org/runs/$id" params={{ org, id: w.current_job_id }} className="relative z-10 block truncate font-mono hover:text-primary">
            {w.current_job_id.slice(0, 8)}
          </Link>
        ) : (
          <span className={CELL_MINOR}>—</span>
        ),
    },
  ];
}
