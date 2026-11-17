import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useDeleteWorker, useRevokeWorker, useWithdrawWorkerEnrollment } from '../../api/hooks/useWorkers.ts';
import type { Worker, WorkerEnrollment } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * useWorkerListActions holds the Workers page's two row menus, both for a
 * writer only (a reader gets no menu). A worker: Revoke… for a live one and
 * Delete… for a revoked one, each asking first. A pending enrollment:
 * Withdraw, without a question (an unredeemed token has no running jobs and
 * no history to lose).
 */
export function useWorkerListActions(org: string, canWrite: boolean): { workerActions: (w: Worker) => RowAction[]; enrollmentActions: (e: WorkerEnrollment) => RowAction[] } {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const revoke = useRevokeWorker(org);
  const remove = useDeleteWorker(org);
  const withdraw = useWithdrawWorkerEnrollment(org);

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

  return {
    workerActions: (w) => {
      if (!canWrite) return [];
      return w.status !== 'revoked'
        ? [{ id: 'revoke', label: t.workers.revokeItem, destructive: true, onSelect: () => void onRevoke(w) }]
        : [{ id: 'delete', label: t.workers.deleteItem, destructive: true, onSelect: () => void onDelete(w) }];
    },
    enrollmentActions: (e) => (canWrite ? [{ id: 'withdraw', label: t.workers.withdraw, onSelect: () => void onWithdraw(e) }] : []),
  };
}
