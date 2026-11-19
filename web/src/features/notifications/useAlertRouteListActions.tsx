import { useState, type ReactNode } from 'react';
import { useDeleteAlertRoute, useUpdateAlertRoute } from '../../api/hooks/useAlertRoutes.ts';
import type { AlertRoute } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { routeToInput } from './alertRoute.ts';

/**
 * useAlertRouteListActions is the alert routes list's row menu, for a writer
 * (`channels.write`) only: Edit, Enable or Disable (what the row's switch
 * was) and Delete. `renderDialog` draws the route dialog an entry or
 * `openNew` opened. The toggle is pending per route.
 */
export function useAlertRouteListActions(
  org: string,
  canWrite: boolean,
  renderDialog: (route: AlertRoute | undefined, onClose: () => void) => ReactNode,
): { rowActions: (route: AlertRoute) => RowAction[]; dialogs: ReactNode; openNew: () => void; edit?: (row: AlertRoute) => void } {
  const t = useT();
  const a = t.notifications.alertRoutes;
  const toast = useToast();
  const update = useUpdateAlertRoute(org);
  const remove = useDeleteAlertRoute(org);
  const [editing, setEditing] = useState<AlertRoute | 'new' | null>(null);
  const [toggling, setToggling] = useState<ReadonlySet<string>>(new Set());
  const del = useConfirmedDelete<AlertRoute>({
    message: (r) => a.deleteConfirm(r.name),
    failed: t.notifications.deleteFailed,
    remove: async (id) => {
      await remove.mutateAsync(id);
    },
  });

  // PATCH is a full replace, so the toggle has to send the whole route
  // back, not the handful of fields it happens to remember: rebuilt by
  // hand here, it silently deleted the delivery window, the breakthrough
  // severities and the rotation, and 400'd outright on a rotation route
  // (whole-branch review, Important #3). routeToInput is the one place
  // that knows what a stored route is on the wire, so a field added to
  // the route in future cannot be forgotten at this call site.
  async function toggle(r: AlertRoute) {
    setToggling((p) => new Set(p).add(r.id));
    try {
      await update.mutateAsync({ ...routeToInput(r), id: r.id, revision: r.revision, enabled: !r.enabled });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : a.updateFailed, 'destructive');
    } finally {
      setToggling((p) => {
        const next = new Set(p);
        next.delete(r.id);
        return next;
      });
    }
  }

  function rowActions(r: AlertRoute): RowAction[] {
    if (!canWrite) return [];
    return [
      { id: 'edit', label: t.notifications.edit, onSelect: () => setEditing(r) },
      { id: 'toggle', label: r.enabled ? a.disable : a.enable, onSelect: () => void toggle(r), disabled: toggling.has(r.id) },
      { id: 'delete', label: t.common.delete, destructive: true, onSelect: () => void del.run(r), disabled: del.pending.has(r.id) },
    ];
  }

  return {
    rowActions,
    edit: canWrite ? setEditing : undefined,
    openNew: () => setEditing('new'),
    dialogs: editing && renderDialog(editing === 'new' ? undefined : editing, () => setEditing(null)),
  };
}
