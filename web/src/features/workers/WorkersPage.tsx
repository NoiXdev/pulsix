import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Cpu } from 'lucide-react';
import { Card, GridHeader } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useDeleteWorker, useRevokeWorker, useWithdrawWorkerEnrollment, useWorkerEnrollments, useWorkers } from '../../api/hooks/useWorkers.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import type { Worker, WorkerEnrollment } from '../../api/types.ts';
import { AddWorkerDialog } from './AddWorkerDialog.tsx';
import { EnrollmentRow, WorkerRow } from './WorkerRow.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';

// The trailing 40px track holds a row's ⋯ menu, only reserved for canWrite.
const COLUMNS_BASE = 'minmax(0, 0.9fr) minmax(0, 0.6fr) 100px 110px minmax(0, 1fr) 110px minmax(0, 0.7fr)';
const COLUMNS_WITH_MENU = `${COLUMNS_BASE} 40px`;
const ENROLLMENT_COLUMNS = 'minmax(0, 1fr) 160px';

/** WorkersPage lists the organization's zone workers (spec 2026-09-18 §4):
 * status (online = seen within 2 minutes), zone, last seen, version,
 * certificate expiry (highlighted under 14 days) and the current job, plus
 * the pending enrollment tokens. Owners and admins add, revoke and delete. */
export function WorkersPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
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
  const columns = canWrite ? COLUMNS_WITH_MENU : COLUMNS_BASE;
  const enrollmentColumns = canWrite ? `${ENROLLMENT_COLUMNS} 40px` : ENROLLMENT_COLUMNS;

  async function onRevoke(w: Worker) {
    if (!(await ask({ message: t.workers.revokeConfirm(w.name), destructive: true }))) return;
    try {
      await revoke.mutateAsync(w.id);
      toast.show(t.workers.revoked(w.name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.workers.revokeFailed, 'destructive');
    }
  }

  async function onDelete(w: Worker) {
    if (!(await ask({ message: t.workers.deleteConfirm(w.name), confirmLabel: t.common.delete, destructive: true }))) return;
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
          {canWrite && <span />}
        </GridHeader>
        {workers.map((w) => (
          <WorkerRow key={w.id} org={org} worker={w} columns={columns} now={now} canWrite={canWrite} onRevoke={(x) => void onRevoke(x)} onDelete={(x) => void onDelete(x)} />
        ))}
        {workers.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.workers.empty}</p>}
      </Card>
      {enrollments.length > 0 && (
        <Card>
          <GridHeader columns={enrollmentColumns}>
            <span>{t.workers.columns.pendingEnrollment}</span>
            <span>{t.workers.columns.expires}</span>
            {canWrite && <span />}
          </GridHeader>
          {enrollments.map((e) => (
            <EnrollmentRow key={e.id} enrollment={e} columns={enrollmentColumns} canWrite={canWrite} onWithdraw={(x) => void onWithdraw(x)} />
          ))}
        </Card>
      )}
      {adding && <AddWorkerDialog org={org} onClose={() => setAdding(false)} />}
    </>
  );
}
