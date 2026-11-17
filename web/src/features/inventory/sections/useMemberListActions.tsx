import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import type { RowAction } from '../../../ui/data-list/index.ts';
import { useConfirm } from '../../../ui/confirmContext.ts';
import { useToast } from '../../../ui/toastContext.ts';
import { useUnassignHost } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup, InventoryMember } from '../../../api/types.ts';
import { useT } from '../../../i18n/useT.ts';

/**
 * useMemberListActions is the row menu of a group's members: Open host for
 * every role and, for a writer, Edit values and Remove from group… (which
 * asks first). Remove is tracked per row, so one pending removal disables
 * only its own entry.
 */
export function useMemberListActions(org: string, group: InventoryGroup, canWrite: boolean, onEdit: (m: InventoryMember) => void): (m: InventoryMember) => RowAction[] {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const navigate = useNavigate();
  const unassign = useUnassignHost(org, group.id);
  const [removing, setRemoving] = useState<ReadonlySet<string>>(new Set());
  const m = t.inventory.members;

  async function remove(member: InventoryMember) {
    if (removing.has(member.host_id)) return;
    if (!(await ask({ message: m.confirmRemove(member.host_name, group.name), confirmLabel: t.common.remove, destructive: true }))) return;
    setRemoving((p) => new Set(p).add(member.host_id));
    try {
      await unassign.mutateAsync(member.host_id);
      toast.show(m.removed(member.host_name, group.name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.removeFailed, 'destructive');
    } finally {
      setRemoving((p) => {
        const next = new Set(p);
        next.delete(member.host_id);
        return next;
      });
    }
  }

  return (member) => {
    const items: RowAction[] = [
      { id: 'open', label: m.openHost, onSelect: () => void navigate({ to: '/o/$org/hosts/$id', params: { org, id: member.host_id } }) },
    ];
    if (canWrite) {
      items.push({ id: 'edit', label: m.editValues, onSelect: () => onEdit(member) });
      items.push({ id: 'remove', label: m.removeItem, destructive: true, onSelect: () => void remove(member), disabled: removing.has(member.host_id) });
    }
    return items;
  };
}
