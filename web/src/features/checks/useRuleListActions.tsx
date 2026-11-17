import { useState, type ReactNode } from 'react';
import { useDeleteRule } from '../../api/hooks/useRules.ts';
import type { Rule } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';

/**
 * useRuleListActions is the rules list's row menu, for a writer only: Edit
 * and Delete…. `renderDialog` draws the rule dialog an entry or `openNew`
 * opened.
 */
export function useRuleListActions(
  org: string,
  canWrite: boolean,
  renderDialog: (rule: Rule | undefined, onClose: () => void) => ReactNode,
): { rowActions: (rule: Rule) => RowAction[]; dialogs: ReactNode; openNew: () => void } {
  const t = useT();
  const remove = useDeleteRule(org);
  const [editing, setEditing] = useState<{ rule?: Rule } | null>(null);
  const del = useConfirmedDelete<Rule>({
    message: (r) => `${t.checks.rules.deleteConfirm(r.name)}${r.origin === 'seed' ? t.checks.rules.deleteSeeded : ''}`,
    failed: t.checks.deleteFailed,
    remove: (id) => remove.mutateAsync(id),
  });

  function rowActions(r: Rule): RowAction[] {
    if (!canWrite) return [];
    return [
      { id: 'edit', label: t.checks.edit, onSelect: () => setEditing({ rule: r }) },
      { id: 'delete', label: t.checks.deleteItem, destructive: true, onSelect: () => void del.run(r), disabled: del.pending.has(r.id) },
    ];
  }

  return {
    rowActions,
    openNew: () => setEditing({}),
    dialogs: editing && renderDialog(editing.rule, () => setEditing(null)),
  };
}
