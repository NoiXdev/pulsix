import { useState, type ReactNode } from 'react';
import { useDeleteMonitoringGroup } from '../../api/hooks/useChecks.ts';
import type { MonitoringGroup } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';

/**
 * useMonitoringGroupListActions is the monitoring groups list's row menu, for
 * a writer only: Edit and Delete…. `renderDialog` draws the group dialog an
 * entry or `openNew` opened.
 */
export function useMonitoringGroupListActions(
  org: string,
  canWrite: boolean,
  renderDialog: (group: MonitoringGroup | undefined, onClose: () => void) => ReactNode,
): { rowActions: (group: MonitoringGroup) => RowAction[]; dialogs: ReactNode; openNew: () => void } {
  const t = useT();
  const remove = useDeleteMonitoringGroup(org);
  const [editing, setEditing] = useState<MonitoringGroup | 'new' | null>(null);
  const del = useConfirmedDelete<MonitoringGroup>({
    message: (g) => t.checks.groups.deleteConfirm(g.name),
    failed: t.checks.deleteFailed,
    remove: (id) => remove.mutateAsync(id),
  });

  function rowActions(g: MonitoringGroup): RowAction[] {
    if (!canWrite) return [];
    return [
      { id: 'edit', label: t.checks.edit, onSelect: () => setEditing(g) },
      { id: 'delete', label: t.checks.deleteItem, destructive: true, onSelect: () => void del.run(g), disabled: del.pending.has(g.id) },
    ];
  }

  return {
    rowActions,
    openNew: () => setEditing('new'),
    dialogs: editing && renderDialog(editing === 'new' ? undefined : editing, () => setEditing(null)),
  };
}
