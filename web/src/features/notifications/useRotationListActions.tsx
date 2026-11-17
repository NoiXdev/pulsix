import { useState, type ReactNode } from 'react';
import { useDeleteOncallRotation } from '../../api/hooks/useOncall.ts';
import type { OncallRotation } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';

/**
 * useRotationListActions is the rotation list's row menu, for a writer
 * (`channels.write`) only: Edit and Delete. `renderDialog` draws the
 * rotation dialog an entry or `openNew` opened.
 */
export function useRotationListActions(
  org: string,
  canWrite: boolean,
  renderDialog: (rotation: OncallRotation | undefined, onClose: () => void) => ReactNode,
): { rowActions: (rotation: OncallRotation) => RowAction[]; dialogs: ReactNode; openNew: () => void } {
  const t = useT();
  const remove = useDeleteOncallRotation(org);
  const [editing, setEditing] = useState<OncallRotation | 'new' | null>(null);
  const del = useConfirmedDelete<OncallRotation>({
    message: (r) => t.notifications.rotations.deleteConfirm(r.name),
    failed: t.notifications.deleteFailed,
    remove: async (id) => {
      await remove.mutateAsync(id);
    },
  });

  function rowActions(r: OncallRotation): RowAction[] {
    if (!canWrite) return [];
    return [
      { id: 'edit', label: t.notifications.edit, onSelect: () => setEditing(r) },
      { id: 'delete', label: t.common.delete, destructive: true, onSelect: () => void del.run(r), disabled: del.pending.has(r.id) },
    ];
  }

  return {
    rowActions,
    openNew: () => setEditing('new'),
    dialogs: editing && renderDialog(editing === 'new' ? undefined : editing, () => setEditing(null)),
  };
}
