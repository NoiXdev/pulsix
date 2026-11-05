import { Link } from '@tanstack/react-router';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem, StaticListRow } from '../../ui/list-row.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import type { Worker, WorkerEnrollment } from '../../api/types.ts';
import { VersionSkewBadge } from './VersionSkewBadge.tsx';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from './expiry.ts';

const STATUS_TONE: Record<string, Tone> = { online: 'ok', offline: 'warn', revoked: 'unknown' };

/** WorkerRow is one zone worker. It opens nothing (there is no worker page),
 * so its cells keep the current-job link. A writer's ⋯ menu offers Revoke… for
 * a live worker and Delete… for a revoked one; `columns` reserves the menu's
 * 40px as its last track, left empty here. */
export function WorkerRow({
  org, worker: w, columns, now, canWrite, onRevoke, onDelete,
}: {
  org: string;
  worker: Worker;
  columns: string;
  now: Date;
  canWrite: boolean;
  onRevoke: (w: Worker) => void;
  onDelete: (w: Worker) => void;
}) {
  const t = useT();
  const expiring = !!w.cert_expires_at && daysLeft(w.cert_expires_at, now) < EXPIRY_WARN_DAYS;
  const statusLabel = w.status in t.workers.status ? t.workers.status[w.status as keyof typeof t.workers.status] : w.status;
  return (
    <StaticListRow
      columns={columns}
      menu={
        canWrite && (
          <RowMenu label={t.common.rowActions(w.name)}>
            {w.status !== 'revoked' ? (
              <RowMenuItem onSelect={() => onRevoke(w)} destructive>
                {t.workers.revokeItem}
              </RowMenuItem>
            ) : (
              <RowMenuItem onSelect={() => onDelete(w)} destructive>
                {t.workers.deleteItem}
              </RowMenuItem>
            )}
          </RowMenu>
        )
      }
    >
      <span className="truncate font-mono text-[13px]" data-row>
        {w.name}
      </span>
      <span className="truncate font-mono text-xs">{w.zone_slug}</span>
      <span>
        <Pill tone={STATUS_TONE[w.status] ?? 'unknown'}>{statusLabel}</Pill>
      </span>
      <span className={`text-xs ${w.status === 'offline' ? 'text-[var(--px-warn)]' : 'text-muted-foreground'}`}>{seenLabel(w.last_seen, now, t)}</span>
      <span className="flex min-w-0 items-center gap-2 text-xs">
        <span className="truncate font-mono">{w.version || '—'}</span>
        {/* A revoked worker never connects again: nothing to update. */}
        {w.status !== 'revoked' && <VersionSkewBadge status={w.version_status} />}
      </span>
      <span className={`text-xs ${expiring ? 'font-semibold text-[var(--px-warn)]' : 'text-muted-foreground'}`} data-expiring={expiring || undefined}>
        {w.status === 'revoked' ? '—' : expiryLabel(w.cert_expires_at, now, t)}
      </span>
      <span className="truncate text-xs">
        {w.current_job_id ? (
          <Link to="/o/$org/runs/$id" params={{ org, id: w.current_job_id }} className="font-mono hover:text-primary">
            {w.current_job_id.slice(0, 8)}
          </Link>
        ) : (
          '—'
        )}
      </span>
      {canWrite && <span />}
    </StaticListRow>
  );
}

/** EnrollmentRow is one pending enrollment token; a writer's ⋯ menu offers
 * Withdraw. */
export function EnrollmentRow({
  enrollment: e, columns, canWrite, onWithdraw,
}: {
  enrollment: WorkerEnrollment;
  columns: string;
  canWrite: boolean;
  onWithdraw: (e: WorkerEnrollment) => void;
}) {
  const t = useT();
  const fmt = useFormat();
  return (
    <StaticListRow
      columns={columns}
      menu={
        canWrite && (
          <RowMenu label={t.workers.enrollmentActions(e.name)}>
            <RowMenuItem onSelect={() => onWithdraw(e)}>{t.workers.withdraw}</RowMenuItem>
          </RowMenu>
        )
      }
    >
      <span className="font-mono text-[13px]">{e.name}</span>
      <span className="text-xs text-muted-foreground">{fmt.time(e.expires_at)}</span>
      {canWrite && <span />}
    </StaticListRow>
  );
}
