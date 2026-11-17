import type { Schedule } from '../../api/types.ts';
import { useDeleteSchedule, useScheduleAction } from '../../api/hooks/useSchedules.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';

export interface ScheduleActionsOptions {
  canWrite: boolean;
  /** Opens a schedule's editor. */
  edit: (id: string) => void;
  /** A schedule was deleted: closes its editor when it is the open one. */
  deleted: (id: string) => void;
}

/**
 * useScheduleActions is the schedules list's row menu for every row:
 * Pause or Resume, Edit (the editor panel, as the row's link) and Delete
 * after a confirmation. All are writes; a viewer's rows have no menu and
 * open the editor read-only through the row's link.
 */
export function useScheduleActions(org: string, opts: ScheduleActionsOptions): (s: Schedule) => RowAction[] {
  const t = useT();
  const m = t.schedules;
  const toast = useToast();
  const ask = useConfirm();
  const remove = useDeleteSchedule(org);
  const pause = useScheduleAction(org, 'pause');
  const resume = useScheduleAction(org, 'resume');

  async function toggle(s: Schedule) {
    try {
      await (s.paused ? resume : pause).mutateAsync(s.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : s.paused ? m.editor.resumeFailed : m.editor.pauseFailed, 'destructive');
    }
  }

  async function del(s: Schedule) {
    if (!(await ask({ message: m.editor.deleteConfirm(s.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(s.id);
      opts.deleted(s.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.editor.deleteFailed, 'destructive');
    }
  }

  return (s) =>
    opts.canWrite
      ? [
          { id: 'toggle', label: s.paused ? m.editor.resume : m.editor.pause, onSelect: () => void toggle(s), disabled: pause.isPending || resume.isPending },
          { id: 'edit', label: t.common.edit, onSelect: () => opts.edit(s.id) },
          { id: 'delete', label: t.common.delete, destructive: true, onSelect: () => void del(s), disabled: remove.isPending },
        ]
      : [];
}
