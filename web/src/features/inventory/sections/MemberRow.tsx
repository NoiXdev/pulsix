import { useNavigate } from '@tanstack/react-router';
import { Pill } from '../../../ui/badge.tsx';
import { ListRow, ListRowName, RowMenu, RowMenuItem } from '../../../ui/list-row.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { useUnassignHost } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup, InventoryMember } from '../../../api/types.ts';
import { useT } from '../../../i18n/useT.ts';

/** MemberRow is one member of a group: the whole row opens the host, and its
 * ⋯ menu offers Open host and, for a writer, Edit values and Remove from
 * group…. `columns` reserves the menu's 40px as its last track. */
export function MemberRow({
  org, group, member, columns, canWrite, onEdit,
}: {
  org: string;
  group: InventoryGroup;
  member: InventoryMember;
  columns: string;
  canWrite: boolean;
  onEdit: (m: InventoryMember) => void;
}) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const unassign = useUnassignHost(org, group.id);
  const m = t.inventory.members;

  async function remove() {
    if (!confirm(m.confirmRemove(member.host_name, group.name))) return;
    try {
      await unassign.mutateAsync(member.host_id);
      toast.show(m.removed(member.host_name, group.name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.removeFailed, 'destructive');
    }
  }

  return (
    <ListRow
      to="/o/$org/hosts/$id"
      params={{ org, id: member.host_id }}
      columns={columns}
      label={member.host_name}
      menu={
        <RowMenu label={t.common.rowActions(member.host_name)}>
          <RowMenuItem onSelect={() => void navigate({ to: '/o/$org/hosts/$id', params: { org, id: member.host_id } })}>{m.openHost}</RowMenuItem>
          {canWrite && <RowMenuItem onSelect={() => onEdit(member)}>{m.editValues}</RowMenuItem>}
          {canWrite && (
            <RowMenuItem onSelect={() => void remove()} destructive>
              {m.removeItem}
            </RowMenuItem>
          )}
        </RowMenu>
      }
    >
      <ListRowName className="truncate font-mono text-[13px]">{member.host_name}</ListRowName>
      <span>{member.complete ? <Pill tone="ok">{m.complete}</Pill> : <Pill tone="warn">{m.toFill((member.problems ?? []).length)}</Pill>}</span>
      <span />
    </ListRow>
  );
}
