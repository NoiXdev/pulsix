import { useGroup } from '../../api/hooks/useInventory.ts';
import { Dialog } from '../../ui/dialog.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useT } from '../../i18n/useT.ts';
import type { InventoryMember } from '../../api/types.ts';
import { AssignHostDialog } from '../inventory/sections/AssignHostDialog.tsx';

/** HostGroupDialog is the group-membership dialog of the host page: the
 * shared assignment dialog with the host fixed. Without a member it adds the
 * host to a group of its choosing; with one it edits that membership's
 * values, once the group (whose schema the form is generated from) is read;
 * until then the dialog shows that it is loading, or why the group could not
 * be read. */
export function HostGroupDialog({
  org, host, member, memberOf, onClose,
}: {
  org: string;
  host: { id: string; name: string };
  member?: InventoryMember;
  /** Ids of the groups the host is a direct member of. */
  memberOf: readonly string[];
  onClose: () => void;
}) {
  const t = useT();
  const group = useGroup(org, member?.group_id ?? '');
  const guard = useQueryGuard(group, { enabled: !!member });
  if (!member) return <AssignHostDialog org={org} host={host} existing={memberOf} onClose={onClose} />;
  if (guard || !group.data) {
    return (
      <Dialog open onClose={onClose} size="wide" title={t.inventory.assign.titleValues(member.host_name)}>
        {guard ?? <QueryLoading />}
      </Dialog>
    );
  }
  return <AssignHostDialog org={org} group={group.data.group} host={host} member={member} existing={[]} onClose={onClose} />;
}
