import { useState } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { Pencil } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { SidebarFacts, SidebarHeading } from '../../ui/detail-layout.tsx';
import { RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useDeleteGroup, useGroupMembers } from '../../api/hooks/useInventory.ts';
import { useBastions } from '../../api/hooks/useSsh.ts';
import type { InventoryGroup, InventoryGroupSummary } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { GroupSettingsDialog } from './GroupSettingsDialog.tsx';

const NONE = '—';

/**
 * GroupSidebar is the group page's left column: the group's name with its
 * completeness, its description, the facts (parent, members, child groups,
 * fields, jump host) and, for a writer, Edit and ⋯ → Delete group…. The
 * section nav below it is DetailLayout's.
 */
export function GroupSidebar({
  org, group, groups, role,
}: {
  org: string;
  group: InventoryGroup;
  groups: readonly InventoryGroupSummary[];
  role: string;
}) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const remove = useDeleteGroup(org);
  const [editing, setEditing] = useState(false);
  const canWrite = may(role, 'inventory.write');
  const own = groups.find((g) => g.id === group.id);

  async function onDelete() {
    if (!confirm(t.inventory.settings.confirmDelete(group.name))) return;
    try {
      await remove.mutateAsync(group.id);
      toast.show(t.inventory.settings.deleted(group.name));
      await navigate({ to: '/o/$org/groups', params: { org } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.inventory.settings.deleteFailed, 'destructive');
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <SidebarHeading
        title={group.name}
        mono
        pill={own && (own.complete ? <Pill tone="ok">{t.inventory.detail.complete}</Pill> : <Pill tone="warn">{t.inventory.detail.incomplete}</Pill>)}
      >
        {group.description || undefined}
      </SidebarHeading>
      <GroupFacts org={org} group={group} groups={groups} />
      {canWrite && (
        <div className="flex flex-wrap items-center gap-2">
          <Button onClick={() => setEditing(true)}>
            <Pencil size={14} /> {t.inventory.detail.edit}
          </Button>
          <RowMenu label={t.inventory.detail.more(group.name)}>
            <RowMenuItem onSelect={() => void onDelete()} destructive>
              {t.inventory.settings.delete}
            </RowMenuItem>
          </RowMenu>
        </div>
      )}
      {editing && (
        <GroupSettingsDialog
          org={org}
          group={group}
          groups={groups}
          // The jump-host create row posts to /bastions, which needs
          // credentials.write (admin), not the inventory.write of the save.
          canCreateCredential={may(role, 'credentials.write')}
          onClose={() => setEditing(false)}
        />
      )}
    </div>
  );
}

/** Parent, members (with how many still need values), child groups, the
 * schema's fields and the group's own jump host. */
function GroupFacts({ org, group, groups }: { org: string; group: InventoryGroup; groups: readonly InventoryGroupSummary[] }) {
  const t = useT();
  const f = t.inventory.detail.fact;
  const members = useGroupMembers(org, group.id);
  const bastions = useBastions(org);
  const own = groups.find((g) => g.id === group.id);
  const parent = groups.find((g) => g.id === group.parent_id);
  const children = groups.filter((g) => g.parent_id === group.id).length;

  const list = members.data?.members;
  let membersValue: string = own ? String(own.members) : NONE;
  if (list) {
    const toFill = list.filter((m) => !m.complete).length;
    membersValue = toFill > 0 ? `${list.length}, ${t.inventory.members.toFill(toFill)}` : String(list.length);
  }
  const bastion = group.bastion_id ? (bastions.data?.bastions?.find((b) => b.id === group.bastion_id)?.name ?? NONE) : NONE;

  return (
    <SidebarFacts
      items={[
        {
          label: f.parent,
          value: parent ? (
            <Link to="/o/$org/groups/$id" params={{ org, id: parent.id }} search={{ tab: undefined }} className="font-mono hover:text-primary">
              {parent.name}
            </Link>
          ) : (
            NONE
          ),
        },
        { label: f.members, value: membersValue },
        { label: f.children, value: String(children) },
        { label: f.fields, value: own ? t.inventory.groups.fields(own.group_fields, own.host_fields) : NONE },
        { label: f.jumpHost, value: <span className="font-mono">{bastion}</span> },
      ]}
    />
  );
}
