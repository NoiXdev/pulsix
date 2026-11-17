import { useState, type ReactNode } from 'react';
import { useDeleteCheck } from '../../api/hooks/useChecks.ts';
import type { CheckDefinition, MonitoringGroup } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { CheckDialog } from './CheckDialog.tsx';

/**
 * useCheckListActions is the definitions list's row menu, for a writer only:
 * Edit, Copy for a seeded or shared definition (its key and plugin are
 * fixed, so customising it means copying) and Delete… for anything but a
 * shared one (a projection of an installation-owned definition cannot be
 * deleted from inside an organization, spec v0.2f decision 6: the server
 * refuses it with 403, and an entry that always fails is not offered).
 * `dialogs` holds the check dialog an entry or `openNew` opened.
 */
export function useCheckListActions(
  org: string,
  opts: { canWrite: boolean; groups: MonitoringGroup[] },
): { rowActions: (check: CheckDefinition) => RowAction[]; dialogs: ReactNode; openNew: () => void } {
  const t = useT();
  const remove = useDeleteCheck(org);
  // `copy` distinguishes "customise this seeded row" from "edit it": the
  // dialog creates in the first case and updates in the second.
  const [editing, setEditing] = useState<{ check?: CheckDefinition; copy?: boolean } | null>(null);
  const del = useConfirmedDelete<CheckDefinition>({
    message: (c) => `${t.checks.list.deleteConfirm(c.key)}${c.origin === 'seed' ? t.checks.list.deleteSeeded : ''}`,
    failed: t.checks.deleteFailed,
    remove: (id) => remove.mutateAsync(id),
  });

  function rowActions(c: CheckDefinition): RowAction[] {
    if (!opts.canWrite) return [];
    const items: RowAction[] = [{ id: 'edit', label: t.checks.edit, onSelect: () => setEditing({ check: c }) }];
    if (c.origin === 'seed' || c.origin === 'shared') items.push({ id: 'copy', label: t.checks.copy, onSelect: () => setEditing({ check: c, copy: true }) });
    if (c.origin !== 'shared') items.push({ id: 'delete', label: t.checks.deleteItem, destructive: true, onSelect: () => void del.run(c), disabled: del.pending.has(c.id) });
    return items;
  }

  return {
    rowActions,
    openNew: () => setEditing({}),
    dialogs: editing && <CheckDialog org={org} check={editing.check} copy={editing.copy} groups={opts.groups} onClose={() => setEditing(null)} />,
  };
}
