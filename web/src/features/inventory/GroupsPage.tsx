import { useState } from 'react';
import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Layers } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { useBastions } from '../../api/hooks/useSsh.ts';
import { validateListSearch } from '../lists/listSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { CreateGroupDialog } from './CreateGroupDialog.tsx';
import { groupColumns } from './groupColumns.tsx';
import { orderTree } from './tree.ts';
import { useT } from '../../i18n/useT.ts';

/** GroupsPage lists the organization's Ansible inventory groups (list id
 * `groups`): the whole tree is one request, shown in tree order and sorted
 * and paged in the browser. A row opens the group; there is no row menu. */
export function GroupsPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const groups = useGroups(org);
  const bastions = useBastions(org);
  const [createOpen, setCreateOpen] = useState(false);

  const all = groups.data?.groups;
  const tree = orderTree(all ?? []);
  const depths = new Map(tree.map(({ group, depth }) => [group.id, depth]));
  const bastionName = (id?: string | null) => bastions.data?.bastions?.find((b) => b.id === id)?.name ?? '—';
  const byId = new Map((all ?? []).map((g) => [g.id, g]));
  const parentName = (id: string) => byId.get(byId.get(id)?.parent_id ?? '')?.name ?? '';
  // The list sorts only by a key one of its columns offers (useListView ignores
  // any other `?sort=`), so only such a key flattens the tree.
  const urlSort = validateListSearch(useSearch({ strict: false }) as Record<string, unknown>).sort;
  const groupCtx = { t, bastionName, depthOf: (id: string) => depths.get(id) ?? 0, parentName };
  const sorted = !!urlSort && groupColumns({ ...groupCtx, sorted: false }).some((c) => c.sortKey === urlSort);
  const columns = groupColumns({ ...groupCtx, sorted });
  const view = useListView('groups', columns, { mode: 'client', rowKey: (g) => g.id });

  const guard = useQueryGuard(groups);
  if (guard) return guard;
  if (!all) return <QueryLoading />;
  const { rows, total } = view.view(view.ready ? tree.map((r) => r.group) : undefined);

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">{t.inventory.groups.title}</h1>
        <Button variant="primary" onClick={() => setCreateOpen(true)}>
          <Layers size={15} /> {t.inventory.groups.addGroup}
        </Button>
      </div>
      <span className="text-[13px] text-muted-foreground">{t.inventory.groups.summary(all.length)}</span>
      <DataList
        label={t.inventory.groups.label}
        columns={columns}
        rows={rows}
        rowKey={(g) => g.id}
        rowLink={(g) => ({ to: '/o/$org/groups/$id', params: { org, id: g.id }, search: { tab: undefined }, label: parentName(g.id) && sorted ? `${g.name}, ${t.inventory.groups.inParent(parentName(g.id))}` : g.name })}
        rowLabel={(g) => g.name}
        loading={!view.ready}
        empty={t.inventory.groups.empty}
        {...view.listProps(total, t.inventory.groups.count)}
      />
      <CreateGroupDialog
        org={org}
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        groups={all}
        onCreated={(id) => {
          setCreateOpen(false);
          void navigate({ to: '/o/$org/groups/$id', params: { org, id }, search: { tab: undefined }, hash: 'schema' });
        }}
      />
    </>
  );
}
