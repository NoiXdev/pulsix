import { useState, type ReactNode } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { FolderPlus, ListTree, UserPlus } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useDeleteGroup, useGroupMembers, useInventoryPreview } from '../../api/hooks/useInventory.ts';
import { useBastions } from '../../api/hooks/useSsh.ts';
import type { InventoryGroup, InventoryGroupSummary } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { isRecord } from '../variables/secrets.ts';
import { useT } from '../../i18n/useT.ts';
import { CreateGroupDialog } from './CreateGroupDialog.tsx';
import { GroupSettingsDialog } from './GroupSettingsDialog.tsx';
import { AssignHostDialog } from './sections/AssignHostDialog.tsx';
import { ancestorsOf } from './tree.ts';

const NONE = '—';

interface GroupHeaderProps {
  org: string;
  group: InventoryGroup;
  groups: readonly InventoryGroupSummary[];
  role: string;
  /** Opens the group's fields (the "Manage fields …" button). */
  onManageFields: () => void;
}

/**
 * GroupHeader is the top of the group page, full width above its tabs: the
 * name with its completeness, the description, the facts (parent, hosts,
 * subgroups, jump host) and on the right the actions — Assign host,
 * Subgroup …, Manage fields … and the "⋯" menu with Edit … and Delete ….
 * Every write needs `inventory.write`; a reader still opens the fields,
 * read-only ("Fields …").
 */
export function GroupHeader({ org, group, groups, role, onManageFields }: GroupHeaderProps) {
  const t = useT();
  const own = groups.find((g) => g.id === group.id);
  return (
    <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
      <div className="flex min-w-0 flex-1 flex-col gap-2.5">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h1 className="min-w-0 font-mono text-xl font-bold break-all">{group.name}</h1>
          {own && (own.complete ? <Pill tone="ok">{t.inventory.detail.complete}</Pill> : <Pill tone="warn">{t.inventory.detail.incomplete}</Pill>)}
        </div>
        {group.description && <p className="text-[13px] text-muted-foreground">{group.description}</p>}
        <GroupFacts org={org} group={group} groups={groups} />
      </div>
      <GroupHeaderActions org={org} group={group} groups={groups} role={role} onManageFields={onManageFields} />
    </div>
  );
}

/** The header's facts: the parent (a link), the hosts — direct, with how
 * many still need values, and with the subgroups' when the preview says —
 * the subgroups and the jump host, the group's own or the one it inherits
 * from the nearest ancestor that sets one. */
function GroupFacts({ org, group, groups }: { org: string; group: InventoryGroup; groups: readonly InventoryGroupSummary[] }) {
  const t = useT();
  const f = t.inventory.groupPage.fact;
  const members = useGroupMembers(org, group.id);
  const bastions = useBastions(org);
  // The preview renders every managed host in the group's subtree, each
  // once: the one count of distinct hosts with the subgroups' the SPA has.
  // The Variables and Inventory tabs read the same query.
  const preview = useInventoryPreview(org, group.id);
  const own = groups.find((g) => g.id === group.id);
  const parent = groups.find((g) => g.id === group.parent_id);
  const subgroups = groups.filter((g) => g.parent_id === group.id).length;

  const list = members.data?.members;
  let hosts: string = own ? String(own.members) : NONE;
  if (list) {
    const toFill = list.filter((m) => !m.complete).length;
    hosts = toFill > 0 ? `${list.length}, ${t.inventory.members.toFill(toFill)}` : String(list.length);
  }
  const meta = subgroups > 0 ? preview.data?.inventory?._meta : undefined;
  if (isRecord(meta) && isRecord(meta.hostvars)) hosts += ` · ${t.inventory.groupPage.withSubgroups(Object.keys(meta.hostvars).length)}`;

  // "—" only when no group on the path sets one: a list still loading says
  // so, and a jump host missing from it shows a piece of its id.
  const setter = group.bastion_id ? undefined : ancestorsOf(groups, group.id).reverse().find((g) => g.bastion_id);
  const bastionId = group.bastion_id ?? setter?.bastion_id;
  let bastion = NONE;
  if (bastionId) {
    const name = bastions.data?.bastions?.find((b) => b.id === bastionId)?.name;
    bastion = name ?? (bastions.isPending ? t.common.loading : bastionId.slice(0, 8));
    if (setter) bastion = t.inventory.groupPage.inheritedJumpHost(bastion, setter.name);
  }

  const items: { label: string; value: ReactNode }[] = [
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
    { label: f.hosts, value: hosts },
    { label: f.subgroups, value: String(subgroups) },
    { label: f.jumpHost, value: <span className="font-mono">{bastion}</span> },
  ];

  return (
    <dl data-testid="group-facts" className="flex flex-wrap items-center gap-x-5 gap-y-1 text-[13px]">
      {items.map((item) => (
        <div key={item.label} className="flex min-w-0 items-center gap-1.5">
          <dt className="text-muted-foreground">{item.label}</dt>
          <dd className="min-w-0 break-words">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** The header's actions and the dialogs they open. */
function GroupHeaderActions({ org, group, groups, role, onManageFields }: GroupHeaderProps) {
  const t = useT();
  const p = t.inventory.groupPage;
  const toast = useToast();
  const ask = useConfirm();
  const navigate = useNavigate();
  const remove = useDeleteGroup(org);
  const members = useGroupMembers(org, group.id);
  const [dialog, setDialog] = useState<'assign' | 'subgroup' | 'edit' | null>(null);
  const canWrite = may(role, 'inventory.write');

  async function onDelete() {
    if (!(await ask({ message: t.inventory.settings.confirmDelete(group.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(group.id);
      toast.show(t.inventory.settings.deleted(group.name));
      await navigate({ to: '/o/$org/groups', params: { org } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.inventory.settings.deleteFailed, 'destructive');
    }
  }

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-2">
      {canWrite && (
        // Disabled until the members are known: the picker leaves them out.
        <Button variant="primary" disabled={!members.data} onClick={() => setDialog('assign')}>
          <UserPlus size={14} /> {p.assignHost}
        </Button>
      )}
      {canWrite && (
        <Button onClick={() => setDialog('subgroup')}>
          <FolderPlus size={14} /> {p.subgroup}
        </Button>
      )}
      <Button onClick={onManageFields}>
        <ListTree size={14} /> {canWrite ? p.manageFields : p.viewFields}
      </Button>
      {canWrite && (
        <RowMenu
          label={t.inventory.detail.more(group.name)}
          triggerClassName="h-[34px] w-[34px] rounded-md border border-border bg-card text-foreground hover:bg-accent"
        >
          <RowMenuItem onSelect={() => setDialog('edit')}>{p.edit}</RowMenuItem>
          <RowMenuItem onSelect={() => void onDelete()} destructive>
            {p.delete}
          </RowMenuItem>
        </RowMenu>
      )}
      {dialog === 'assign' && (
        <AssignHostDialog org={org} group={group} existing={(members.data?.members ?? []).map((m) => m.host_id)} onClose={() => setDialog(null)} />
      )}
      {canWrite && (
        <CreateGroupDialog
          org={org}
          open={dialog === 'subgroup'}
          onClose={() => setDialog(null)}
          groups={groups}
          fixedParent={group.id}
          onCreated={(id) => {
            setDialog(null);
            // A new group has no fields yet: its page opens on them.
            void navigate({ to: '/o/$org/groups/$id', params: { org, id }, search: { tab: undefined }, hash: 'schema' });
          }}
        />
      )}
      {dialog === 'edit' && (
        <GroupSettingsDialog
          org={org}
          group={group}
          groups={groups}
          // The jump-host create row posts to /bastions, which needs
          // credentials.write (admin), not the inventory.write of the save.
          canCreateCredential={may(role, 'credentials.write')}
          onClose={() => setDialog(null)}
        />
      )}
    </div>
  );
}
