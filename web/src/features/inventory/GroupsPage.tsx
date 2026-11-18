import { useState } from 'react';
import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Layers } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { FilterChip, ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { useBastions } from '../../api/hooks/useSsh.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { validateListSearch } from '../lists/listSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { CreateGroupDialog } from './CreateGroupDialog.tsx';
import { groupColumns } from './groupColumns.tsx';
import { filterGroups, GROUP_VALUES, GROUPS_FILTERS, groupPath } from './groupsSearch.ts';
import { orderTree } from './tree.ts';
import { useT } from '../../i18n/useT.ts';

/** GroupsPage lists the organization's Ansible inventory groups (list id
 * `groups`): the whole tree is one request, shown in tree order and
 * filtered, sorted and paged in the browser, the filters in the URL
 * (groupsSearch.ts). A sort or a filter lists the groups flat, each naming
 * its path. A row opens the group; there is no row menu. */
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
  const pathOf = (id: string) => (byId.has(id) ? groupPath(all ?? [], id).join(' › ') : '');
  const { filters, active, setFilter, reset } = useListFilters(GROUPS_FILTERS);
  // The list sorts only by a key one of its columns offers (useListView ignores
  // any other `?sort=`), so only such a key, or a filter, flattens the tree.
  const urlSort = validateListSearch(useSearch({ strict: false }) as Record<string, unknown>).sort;
  const groupCtx = { t, bastionName, depthOf: (id: string) => depths.get(id) ?? 0, pathOf };
  const sorted = !!urlSort && groupColumns({ ...groupCtx, flat: false }).some((c) => c.sortKey === urlSort);
  const flat = sorted || active;
  const columns = groupColumns({ ...groupCtx, flat });
  const view = useListView('groups', columns, { mode: 'client', rowKey: (g) => g.id, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(groups);
  if (guard) return guard;
  if (!all) return <QueryLoading />;
  const ordered = tree.map((r) => r.group);
  const shown = filterGroups(ordered, all, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const g = t.inventory.groups;
  const f = g.filters;
  // A parent is worth offering when something sits below it.
  const parents = tree.filter((r) => all.some((c) => c.parent_id === r.group.id));
  const parentOptions = parents.map(({ group: p }) => ({ value: p.id, label: p.name, hint: pathOf(p.id) || undefined }));
  // A parent only the URL names (a stale link) stays pickable so it can be cleared.
  if (filters.parent && !parents.some((r) => r.group.id === filters.parent)) {
    const known = all.find((c) => c.id === filters.parent);
    parentOptions.push({ value: filters.parent, label: known?.name ?? filters.parent.slice(0, 8), hint: undefined });
  }

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
        rowLink={(r) => ({ to: '/o/$org/groups/$id', params: { org, id: r.id }, search: { tab: undefined }, label: pathOf(r.id) && flat ? `${r.name}, ${g.inParent(pathOf(r.id))}` : r.name })}
        rowLabel={(r) => r.name}
        loading={!view.ready}
        empty={active ? g.noMatch : g.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={all.length} noun={g.count} />}>
            <SearchInput label={g.search} placeholder={g.filterPlaceholder} value={filters.q ?? ''} onChange={(q) => setFilter('q', q || undefined, { replace: true })} />
            <FilterSelect
              label={f.parent}
              allLabel={f.anyParent}
              options={parentOptions}
              value={filters.parent}
              onChange={(v) => setFilter('parent', v)}
            />
            <FilterSelect
              label={f.values}
              allLabel={f.anyValues}
              options={GROUP_VALUES.map((v) => ({ value: v, label: t.inventory.detail[v] }))}
              value={filters.values}
              onChange={(v) => setFilter('values', v as typeof filters.values)}
            />
            <FilterChip pressed={!!filters.jump} onToggle={() => setFilter('jump', filters.jump ? undefined : true)}>
              {f.jump}
            </FilterChip>
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, g.count)}
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
