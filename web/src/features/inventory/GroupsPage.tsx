import { useState } from 'react';
import { Link, useNavigate, useParams } from '@tanstack/react-router';
import { Layers } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { useBastions } from '../../api/hooks/useSsh.ts';
import { CreateGroupDialog } from './CreateGroupDialog.tsx';
import { orderTree } from './tree.ts';

const COLUMNS = 'minmax(0, 1fr) 90px 170px 120px 160px';

/** GroupsPage lists the organization's Ansible inventory groups as a tree. */
export function GroupsPage() {
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
        <h1 className="text-xl font-semibold">Groups</h1>
        <Button variant="primary" onClick={() => setCreateOpen(true)}>
          <Layers size={15} /> Add group
        </Button>
      </div>
      <span className="text-[13px] text-muted-foreground">{list.length} inventory groups · each group owns a variable schema</span>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Group</span>
          <span>Members</span>
          <span>Fields</span>
          <span>Values</span>
          <span>Jump host</span>
        </GridHeader>
        {orderTree(list).map(({ group, depth }) => (
          <GridRow key={group.id} columns={COLUMNS}>
            <Link
              to="/o/$org/groups/$id"
              params={{ org, id: group.id }}
              search={{ tab: undefined }}
              className="truncate font-mono text-[13px] text-foreground hover:text-primary"
              style={{ paddingLeft: depth * 16 }}
            >
              {group.name}
            </Link>
            <span className="text-[13px]">{group.members}</span>
            <span className="text-xs text-muted-foreground">
              {group.group_fields} group · {group.host_fields} host
            </span>
            <span>{group.complete ? <Pill tone="ok">complete</Pill> : <Pill tone="warn">incomplete</Pill>}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{bastionName(group.bastion_id)}</span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No inventory groups yet.</p>}
      </Card>
      <CreateGroupDialog
        org={org}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        groups={list}
        onCreated={(id) => {
          setCreateOpen(false);
          void navigate({ to: '/o/$org/groups/$id', params: { org, id }, search: { tab: 'schema' } });
        }}
      />
    </>
  );
}
