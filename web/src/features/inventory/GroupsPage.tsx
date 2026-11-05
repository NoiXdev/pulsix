import { useState } from 'react';
import { useNavigate, useParams } from '@tanstack/react-router';
import { Layers } from 'lucide-react';
import { Card, GridHeader } from '../../ui/table.tsx';
import { ListRow } from '../../ui/list-row.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { useBastions } from '../../api/hooks/useSsh.ts';
import { CreateGroupDialog } from './CreateGroupDialog.tsx';
import { orderTree } from './tree.ts';
import { useT } from '../../i18n/useT.ts';

const COLUMNS = 'minmax(0, 1fr) 90px 170px 120px 160px';

/** GroupsPage lists the organization's Ansible inventory groups as a tree. */
export function GroupsPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const groups = useGroups(org);
  const bastions = useBastions(org);
  const [createOpen, setCreateOpen] = useState(false);

  const guard = useQueryGuard(groups);
  if (guard) return guard;
  if (!groups.data) return <QueryLoading />;
  const list = groups.data.groups ?? [];
  const bastionName = (id?: string | null) => bastions.data?.bastions?.find((b) => b.id === id)?.name ?? '—';

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t.inventory.groups.title}</h1>
        <Button variant="primary" onClick={() => setCreateOpen(true)}>
          <Layers size={15} /> {t.inventory.groups.addGroup}
        </Button>
      </div>
      <span className="text-[13px] text-muted-foreground">{t.inventory.groups.summary(list.length)}</span>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.inventory.groups.columns.group}</span>
          <span>{t.inventory.groups.columns.members}</span>
          <span>{t.inventory.groups.columns.fields}</span>
          <span>{t.inventory.groups.columns.values}</span>
          <span>{t.inventory.groups.columns.jumpHost}</span>
        </GridHeader>
        {orderTree(list).map(({ group, depth }) => (
          <ListRow key={group.id} to="/o/$org/groups/$id" params={{ org, id: group.id }} search={{ tab: undefined }} columns={COLUMNS} label={group.name}>
            <span className="truncate font-mono text-[13px] text-foreground" style={{ paddingLeft: depth * 16 }}>
              {group.name}
            </span>
            <span className="text-[13px]">{group.members}</span>
            <span className="text-xs text-muted-foreground">
              {t.inventory.groups.fields(group.group_fields, group.host_fields)}
            </span>
            <span>{group.complete ? <Pill tone="ok">{t.inventory.groups.complete}</Pill> : <Pill tone="warn">{t.inventory.groups.incomplete}</Pill>}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{bastionName(group.bastion_id)}</span>
          </ListRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.inventory.groups.empty}</p>}
      </Card>
      <CreateGroupDialog
        org={org}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        groups={list}
        onCreated={(id) => {
          setCreateOpen(false);
          void navigate({ to: '/o/$org/groups/$id', params: { org, id }, search: { tab: undefined }, hash: 'schema' });
        }}
      />
    </>
  );
}
