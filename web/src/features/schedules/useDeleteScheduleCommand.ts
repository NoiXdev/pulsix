import { useDeleteSchedule } from '../../api/hooks/useSchedules.ts';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';

/**
 * useDeleteScheduleCommand is deleting a schedule as the list's row menu and
 * the editor both do it: confirm, delete, a toast on failure. `run` resolves
 * true once the schedule is gone.
 */
export function useDeleteScheduleCommand(org: string): { run: (s: { id: string; name: string }) => Promise<boolean>; isPending: boolean } {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const remove = useDeleteSchedule(org);
  async function run(s: { id: string; name: string }): Promise<boolean> {
    if (!(await ask({ message: t.schedules.editor.deleteConfirm(s.name), confirmLabel: t.common.delete, destructive: true }))) return false;
    try {
      await remove.mutateAsync(s.id);
      return true;
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.schedules.editor.deleteFailed, 'destructive');
      return false;
    }
  }
  return { run, isPending: remove.isPending };
}
