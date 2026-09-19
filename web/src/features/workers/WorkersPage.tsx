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
import type { Worker } from '../../api/types.ts';
import { AddWorkerDialog } from './AddWorkerDialog.tsx';
import { EXPIRY_WARN_DAYS, daysLeft, expiryLabel, seenLabel } from './expiry.ts';

const COLUMNS = 'minmax(0, 0.9fr) minmax(0, 0.6fr) 100px 110px minmax(0, 0.6fr) 110px minmax(0, 0.7fr) 150px';
const STATUS_TONE: Record<string, Tone> = { online: 'ok', offline: 'warn', revoked: 'unknown' };

/** WorkersPage lists the organization's zone workers (spec 2026-09-18 §4):
 * status (online = seen within 2 minutes), zone, last seen, version,
 * certificate expiry (highlighted under 14 days) and the current job, plus
 * the pending enrollment tokens. Owners and admins add, revoke and delete. */
export function WorkersPage() {
  const { org } = useParams({ strict: false }) as { org: string };
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
    return <p className="text-sm text-muted-foreground">Workers are managed by the organization's owners and admins.</p>;
  }
  if (!list.data) return <QueryLoading />;
  const workers = list.data.workers ?? [];
  const enrollments = pending.data?.enrollments ?? [];
  const now = new Date();

  async function onRevoke(w: Worker) {
    if (!confirm(`Revoke worker ${w.name}? It is cut off at once; its running jobs go back to the queue.`)) return;
    try {
      await revoke.mutateAsync(w.id);
      toast.show(`Worker ${w.name} revoked`);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Revoking failed', 'destructive');
    }
  }

  async function onDelete(w: Worker) {
    if (!confirm(`Delete worker ${w.name}? Its history stays in the activity log.`)) return;
    try {
      await remove.mutateAsync(w.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">Workers</h1>
          <span className="text-[13px] text-muted-foreground">
            {workers.filter((w) => w.status === 'online').length} of {workers.length} online · workers run the jobs of their zone
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setAdding(true)}>
            <Cpu size={15} /> Add worker
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Worker</span>
          <span>Zone</span>
          <span>Status</span>
          <span>Last seen</span>
          <span>Version</span>
          <span>Certificate</span>
          <span>Current job</span>
          {canWrite && <span>Actions</span>}
        </GridHeader>
        {workers.map((w) => {
          const expiring = !!w.cert_expires_at && daysLeft(w.cert_expires_at, now) < EXPIRY_WARN_DAYS;
          return (
            <GridRow key={w.id} columns={COLUMNS}>
              <span className="truncate font-mono text-[13px]" data-row>
                {w.name}
              </span>
              <span className="truncate font-mono text-xs">{w.zone_slug}</span>
              <span>
                <Pill tone={STATUS_TONE[w.status] ?? 'unknown'}>{w.status}</Pill>
              </span>
              <span className={`text-xs ${w.status === 'offline' ? 'text-[var(--px-warn)]' : 'text-muted-foreground'}`}>{seenLabel(w.last_seen, now)}</span>
              <span className="truncate font-mono text-xs">{w.version || '—'}</span>
              <span className={`text-xs ${expiring ? 'font-semibold text-[var(--px-warn)]' : 'text-muted-foreground'}`} data-expiring={expiring || undefined}>
                {w.status === 'revoked' ? '—' : expiryLabel(w.cert_expires_at, now)}
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
                    <Button type="button" variant="destructive" aria-label={`Revoke ${w.name}`} onClick={() => void onRevoke(w)}>
                      Revoke
                    </Button>
                  ) : (
                    <Button type="button" variant="ghost" aria-label={`Delete ${w.name}`} onClick={() => void onDelete(w)}>
                      Delete
                    </Button>
                  )}
                </span>
              )}
            </GridRow>
          );
        })}
        {workers.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No workers yet.</p>}
      </Card>
      {enrollments.length > 0 && (
        <Card>
          <GridHeader columns="minmax(0, 1fr) 160px 110px">
            <span>Pending enrollment</span>
            <span>Expires</span>
            <span />
          </GridHeader>
          {enrollments.map((e) => (
            <GridRow key={e.id} columns="minmax(0, 1fr) 160px 110px">
              <span className="font-mono text-[13px]">{e.name}</span>
              <span className="text-xs text-muted-foreground">{new Date(e.expires_at).toLocaleTimeString()}</span>
              {canWrite ? (
                <Button type="button" variant="ghost" aria-label={`Withdraw ${e.name}`} onClick={() => void withdraw.mutateAsync(e.id)}>
                  Withdraw
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
