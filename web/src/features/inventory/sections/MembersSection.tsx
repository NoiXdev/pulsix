import { useState } from 'react';
import { DataList } from '../../../ui/data-list/index.ts';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useGroupMembers } from '../../../api/hooks/useInventory.ts';
import type { InventoryGroup, InventoryMember } from '../../../api/types.ts';
import { useListView } from '../../lists/useListView.ts';
import { AssignHostDialog } from './AssignHostDialog.tsx';
import { memberColumns } from './memberColumns.tsx';
import { useMemberListActions } from './useMemberListActions.tsx';
import { useT } from '../../../i18n/useT.ts';

/** MembersSection is the Members tab: the group's direct members (list id
 * `group-members`, sorted and paged in the browser, never in the URL: the
 * page's own list is the group's), each a row to its host page, whose menu
 * edits a member's values for a writer. Assign host sits in the page
 * header. */
export function MembersSection({ org, group, canWrite }: { org: string; group: InventoryGroup; canWrite: boolean }) {
  const members = useGroupMembers(org, group.id);
  const [editing, setEditing] = useState<InventoryMember | null>(null);
  const list = members.data?.members ?? [];

  return (
    <>
      <MembersList org={org} group={group} members={members} canWrite={canWrite} onEdit={setEditing} />
      {editing && (
        <AssignHostDialog org={org} group={group} member={editing} existing={list.map((m) => m.host_id)} onClose={() => setEditing(null)} />
      )}
    </>
  );
}

/** The list itself, with the query's loading and error states in its place
 * while there are no rows to show. */
function MembersList({
  org, group, members, canWrite, onEdit,
}: {
  org: string;
  group: InventoryGroup;
  members: ReturnType<typeof useGroupMembers>;
  canWrite: boolean;
  onEdit: (m: InventoryMember) => void;
}) {
  const t = useT();
  const columns = memberColumns(t);
  const view = useListView('group-members', columns, { mode: 'client', urlState: false, rowKey: (m) => m.host_id });
  const rowActions = useMemberListActions(org, group, canWrite, onEdit);
  const guard = useQueryGuard(members);
  if (guard) return guard;
  if (!members.data) return <QueryLoading />;
  const { rows, total } = view.view(view.ready ? (members.data.members ?? []) : undefined);
  return (
    <DataList
      label={t.inventory.members.label}
      columns={columns}
      rows={rows}
      rowKey={(m) => m.host_id}
      rowLink={(m) => ({ to: '/o/$org/hosts/$id', params: { org, id: m.host_id }, label: m.host_name })}
      rowActions={rowActions}
      loading={!view.ready}
      empty={t.inventory.members.empty}
      {...view.listProps(total, t.inventory.members.count)}
    />
  );
}
