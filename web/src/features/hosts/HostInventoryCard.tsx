import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { Plus } from 'lucide-react';
import { Card } from '../../ui/table.tsx';
import { Pill } from '../../ui/badge.tsx';
import { Button } from '../../ui/button.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useGroups, useHostInventoryGroups, useUnassignHost } from '../../api/hooks/useInventory.ts';
import type { InventoryMember } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { HostGroupDialog } from './HostGroupDialog.tsx';
import { inheritedGroups } from './inheritedGroups.ts';
import { AssistantPill } from '../inventory/AssistantMark.tsx';

/** HostInventoryCard lists the inventory groups a managed host is a member
 * of, with whether its values are complete. The direct groups come first; a
 * writer can add the host to a group and edit or remove each membership.
 * The groups the host is in only through a subgroup follow, muted, without
 * actions. It has no title of its own: the host page's Inventory section
 * names it. */
export function HostInventoryCard({ org, hostId, hostName, managed, canWrite }: { org: string; hostId: string; hostName: string; managed: boolean; canWrite: boolean }) {
  const t = useT();
  const groups = useHostInventoryGroups(org, managed ? hostId : '');
  const all = useGroups(org, { enabled: managed });
  const [dialog, setDialog] = useState<'add' | InventoryMember | null>(null);
  const direct = groups.data?.groups ?? [];
  const inherited = inheritedGroups(all.data?.groups ?? [], direct.map((m) => ({ id: m.group_id, name: m.group_name })));
  return (
    <Card className="flex flex-col gap-3 p-4">
      {!managed && <p className="text-xs text-muted-foreground">{t.hosts.inventory.needsManaged}</p>}
      {managed && !groups.data && groups.isError && (
        <p role="alert" className="text-xs text-destructive">
          {t.hosts.inventory.loadFailed}
        </p>
      )}
      {managed && !groups.data && !groups.isError && <p className="text-xs text-muted-foreground">{t.hosts.inventory.loading}</p>}
      {managed && groups.data && direct.length === 0 && <p className="text-xs text-muted-foreground">{t.hosts.inventory.none}</p>}
      {managed && (
        <ul className="flex flex-col gap-2">
          {direct.map((m) => (
            <DirectRow key={m.group_id} org={org} member={m} canWrite={canWrite} onEdit={() => setDialog(m)} />
          ))}
          {inherited.map((g) => (
            <li key={g.id} className="flex items-center justify-between gap-2 text-muted-foreground" title={t.hosts.inventory.inheritedLabel}>
              <Link to="/o/$org/groups/$id" params={{ org, id: g.id }} search={{ tab: 'members' }} className="font-mono text-xs hover:text-primary">
                {g.name}
              </Link>
              <span className="text-[11px]">{t.hosts.inventory.inheritedVia(g.via)}</span>
            </li>
          ))}
        </ul>
      )}
      {managed && canWrite && groups.data && (
        <div>
          <Button onClick={() => setDialog('add')}>
            <Plus size={14} /> {t.hosts.inventory.addToGroup}
          </Button>
        </div>
      )}
      {dialog && (
        <HostGroupDialog
          org={org}
          host={{ id: hostId, name: dialog === 'add' ? hostName : dialog.host_name }}
          member={dialog === 'add' ? undefined : dialog}
          memberOf={direct.map((m) => m.group_id)}
          onClose={() => setDialog(null)}
        />
      )}
    </Card>
  );
}

function DirectRow({ org, member, canWrite, onEdit }: { org: string; member: InventoryMember; canWrite: boolean; onEdit: () => void }) {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const unassign = useUnassignHost(org, member.group_id);
  const m = t.inventory.members;

  async function remove() {
    if (unassign.isPending) return;
    if (!(await ask({ message: m.confirmRemove(member.host_name, member.group_name), confirmLabel: t.common.remove, destructive: true }))) return;
    try {
      await unassign.mutateAsync(member.host_id);
      toast.show(m.removed(member.host_name, member.group_name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.removeFailed, 'destructive');
    }
  }

  return (
    <li className="flex flex-wrap items-center justify-between gap-2">
      <Link to="/o/$org/groups/$id" params={{ org, id: member.group_id }} search={{ tab: 'members' }} className="font-mono text-xs hover:text-primary">
        {member.group_name}
      </Link>
      <span className="flex items-center gap-2">
        {member.complete ? <Pill tone="ok">{t.hosts.inventory.complete}</Pill> : <Pill tone="warn">{t.hosts.inventory.toFill((member.problems ?? []).length)}</Pill>}
        <AssistantPill keys={member.assistant_keys} />
        {canWrite && (
          <>
            <Button aria-label={t.hosts.inventory.editValuesFor(member.group_name)} onClick={onEdit}>
              {t.hosts.inventory.editValues}
            </Button>
            <Button aria-label={t.hosts.inventory.removeFromGroupFor(member.group_name)} disabled={unassign.isPending} onClick={() => void remove()}>
              {t.hosts.inventory.removeFromGroup}
            </Button>
          </>
        )}
      </span>
    </li>
  );
}
