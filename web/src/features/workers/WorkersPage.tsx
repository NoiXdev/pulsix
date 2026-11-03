import { useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { Cpu } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useDeleteWorker, useRevokeWorker, useWithdrawWorkerEnrollment, useWorkerEnrollments, useWorkers } from '../../api/hooks/useWorkers.ts';
import { may } from '../../lib/roles.ts';
import { useFormat } from '../../i18n/format.ts';
import { VersionSkewBadge } from './VersionSkewBadge.tsx';
import { useT } from '../../i18n/useT.ts';
import type { Worker, WorkerEnrollment } from '../../api/types.ts';
import { AddWorkerDialog } from './AddWorkerDialog.tsx';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from './expiry.ts';

// The trailing 150px track is the Actions column, only rendered for
// canWrite — dropped from the template for read-only viewers so the grid
// doesn't reserve a permanently blank 8th column for them.
const COLUMNS_BASE = 'minmax(0, 0.9fr) minmax(0, 0.6fr) 100px 110px minmax(0, 1fr) 110px minmax(0, 0.7fr)';
const COLUMNS_WITH_ACTIONS = `${COLUMNS_BASE} 150px`;
const STATUS_TONE: Record<string, Tone> = { online: 'ok', offline: 'warn', revoked: 'unknown' };

/** WorkersPage lists the organization's zone workers (spec 2026-09-18 §4):
 * status (online = seen within 2 minutes), zone, last seen, version,
 * certificate expiry (highlighted under 14 days) and the current job, plus
 * the pending enrollment tokens. Owners and admins add, revoke and delete. */
export function WorkersPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const fmt = useFormat();
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'workers.read');
  const canWrite = may(role, 'workers.write');
  const list = useWorkers(org, !!orgInfo.data && canRead);
  const pending = useWorkerEnrollments(org, !!orgInfo.data && canRead);
  const revoke = useRevokeWorker(org);
  const remove = useDeleteWorker(org);
  const withdraw = useWithdrawWorkerEnrollment(org);
  const [adding, setAdding] = useState(false);

  const orgGuard = useQueryGuard(orgInfo);
  if (orgGuard) return orgGuard;
  if (!orgInfo.data) return <QueryLoading />;
  if (!canRead) {
    return <p className="text-sm text-muted-foreground">{t.workers.noAccess}</p>;
  }
  if (!list.data) return <QueryLoading />;
  const workers = list.data.workers ?? [];
  const enrollments = pending.data?.enrollments ?? [];
  const now = new Date();
  const columns = canWrite ? COLUMNS_WITH_ACTIONS : COLUMNS_BASE;
  const statusLabel = (status: string) => (status in t.workers.status ? t.workers.status[status as keyof typeof t.workers.status] : status);

  async function onRevoke(w: Worker) {
    if (!confirm(t.workers.revokeConfirm(w.name))) return;
    try {
      await revoke.mutateAsync(w.id);
      toast.show(t.workers.revoked(w.name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.workers.revokeFailed, 'destructive');
    }
  }

  async function onDelete(w: Worker) {
    if (!confirm(t.workers.deleteConfirm(w.name))) return;
    try {
      await remove.mutateAsync(w.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.workers.deleteFailed, 'destructive');
    }
  }

  async function onWithdraw(e: WorkerEnrollment) {
    try {
      await withdraw.mutateAsync(e.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.workers.withdrawFailed, 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.workers.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.workers.summary(workers.filter((w) => w.status === 'online').length, workers.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Cpu size={15} /> {t.workers.add}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={columns}>
          <span>{t.workers.columns.worker}</span>
          <span>{t.workers.columns.zone}</span>
          <span>{t.workers.columns.status}</span>
          <span>{t.workers.columns.lastSeen}</span>
          <span>{t.workers.columns.version}</span>
          <span>{t.workers.columns.certificate}</span>
          <span>{t.workers.columns.currentJob}</span>
          {canWrite && <span>{t.workers.columns.actions}</span>}
        </GridHeader>
        {workers.map((w) => {
          const expiring = !!w.cert_expires_at && daysLeft(w.cert_expires_at, now) < EXPIRY_WARN_DAYS;
          return (
            <GridRow key={w.id} columns={columns}>
              <span className="truncate font-mono text-[13px]" data-row>
                {w.name}
              </span>
              <span className="truncate font-mono text-xs">{w.zone_slug}</span>
              <span>
                <Pill tone={STATUS_TONE[w.status] ?? 'unknown'}>{statusLabel(w.status)}</Pill>
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
              {canWrite && (
                <span className="flex gap-1">
                  {w.status !== 'revoked' ? (
                    <Button type="button" variant="destructive" aria-label={t.workers.revokeLabel(w.name)} onClick={() => void onRevoke(w)}>
                      {t.workers.revoke}
                    </Button>
                  ) : (
                    <Button type="button" variant="ghost" aria-label={t.workers.deleteLabel(w.name)} onClick={() => void onDelete(w)}>
                      {t.workers.delete}
                    </Button>
                  )}
                </span>
              )}
            </GridRow>
          );
        })}
        {workers.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.workers.empty}</p>}
      </Card>
      {enrollments.length > 0 && (
        <Card>
          <GridHeader columns="minmax(0, 1fr) 160px 110px">
            <span>{t.workers.columns.pendingEnrollment}</span>
            <span>{t.workers.columns.expires}</span>
            <span />
          </GridHeader>
          {enrollments.map((e) => (
            <GridRow key={e.id} columns="minmax(0, 1fr) 160px 110px">
              <span className="font-mono text-[13px]">{e.name}</span>
              <span className="text-xs text-muted-foreground">{fmt.time(e.expires_at)}</span>
              {canWrite ? (
                <Button type="button" variant="ghost" aria-label={t.workers.withdrawLabel(e.name)} onClick={() => void onWithdraw(e)}>
                  {t.workers.withdraw}
                </Button>
              ) : (
                <span />
              )}
            </GridRow>
          ))}
        </Card>
      )}
      {adding && <AddWorkerDialog org={org} onClose={() => setAdding(false)} />}
    </>
  );
}
