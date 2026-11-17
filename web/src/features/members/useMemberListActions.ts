import { useNavigate } from '@tanstack/react-router';
import { useRemoveMember } from '../../api/hooks/useMembers.ts';
import type { Member } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';

/**
 * useMemberListActions is the member list's row menu: Open for everyone, and
 * for a role that manages members Remove…, which asks the same question and
 * ends the member's access to the organization.
 */
export function useMemberListActions(org: string, canManage: boolean): (member: Member) => RowAction[] {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const remove = useRemoveMember(org);
  const del = useConfirmedDelete<Member>({
    rowId: (m) => m.user_id,
    message: (m) => t.members.confirmRemove(m.display_name),
    failed: t.members.removeFailed,
    confirmLabel: t.common.remove,
    remove: async (id) => {
      await remove.mutateAsync(id);
    },
    onDone: (m) => toast.show(t.members.removed(m.display_name)),
  });

  return (m) => {
    const items: RowAction[] = [
      { id: 'open', label: t.members.open, onSelect: () => void navigate({ to: '/o/$org/settings/members/$userId', params: { org, userId: m.user_id } }) },
    ];
    if (canManage) {
      items.push({ id: 'remove', label: t.members.removeItem, destructive: true, onSelect: () => void del.run(m), disabled: del.pending.has(m.user_id) });
    }
    return items;
  };
}
