import { useState, type ReactNode } from 'react';
import { useDeleteItemDecision } from '../../api/hooks/useItems.ts';
import type { ItemDecision } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';

/**
 * useDecisionListActions is the item decisions list's row menu, for a writer
 * only: Edit and Delete…. `renderDialog` draws the decision dialog an entry
 * or `openNew` opened.
 */
export function useDecisionListActions(
  org: string,
  canWrite: boolean,
  renderDialog: (decision: ItemDecision | undefined, onClose: () => void) => ReactNode,
): { rowActions: (decision: ItemDecision) => RowAction[]; dialogs: ReactNode; openNew: () => void } {
  const t = useT();
  const remove = useDeleteItemDecision(org);
  const [editing, setEditing] = useState<{ decision?: ItemDecision } | null>(null);
  const del = useConfirmedDelete<ItemDecision>({
    message: (d) => t.checks.items.deleteConfirm(t.checks.items.verdict[d.decision], d.pattern),
    failed: t.checks.deleteFailed,
    remove: (id) => remove.mutateAsync(id),
  });

  function rowActions(d: ItemDecision): RowAction[] {
    if (!canWrite) return [];
    return [
      { id: 'edit', label: t.checks.edit, onSelect: () => setEditing({ decision: d }) },
      { id: 'delete', label: t.checks.deleteItem, destructive: true, onSelect: () => void del.run(d), disabled: del.pending.has(d.id) },
    ];
  }

  return {
    rowActions,
    openNew: () => setEditing({}),
    dialogs: editing && renderDialog(editing.decision, () => setEditing(null)),
  };
}
