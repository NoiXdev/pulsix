import { useState, type ReactNode } from 'react';
import { useDeleteMaintenanceWindow } from '../../api/hooks/useMaintenance.ts';
import type { MaintenanceWindow } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { WindowDialog } from './WindowDialog.tsx';

/**
 * useWindowListActions is the maintenance windows' row menu, for a writer
 * only: Edit and Delete. `dialogs` holds the window dialog an entry or
 * `openNew` opened.
 */
export function useWindowListActions(org: string, canWrite: boolean): { rowActions: (w: MaintenanceWindow) => RowAction[]; dialogs: ReactNode; openNew: () => void } {
  const t = useT();
  const remove = useDeleteMaintenanceWindow(org);
  const [editing, setEditing] = useState<MaintenanceWindow | 'new' | null>(null);
  const del = useConfirmedDelete<MaintenanceWindow>({
    message: (w) => t.maintenance.deleteConfirm[w.reason === 'silence' ? 'silence' : 'maintenance'](w.name),
    failed: t.maintenance.deleteFailed,
    remove: (id) => remove.mutateAsync(id),
  });

  function rowActions(w: MaintenanceWindow): RowAction[] {
    if (!canWrite) return [];
    return [
      { id: 'edit', label: t.maintenance.edit, onSelect: () => setEditing(w) },
      { id: 'delete', label: t.common.delete, destructive: true, onSelect: () => void del.run(w), disabled: del.pending.has(w.id) },
    ];
  }

  return {
    rowActions,
    openNew: () => setEditing('new'),
    dialogs: editing && <WindowDialog org={org} window={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />,
  };
}
