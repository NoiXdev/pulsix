import { DataList } from '../../../ui/data-list/index.ts';
import type { InventoryGroup, InventoryGroupSummary } from '../../../api/types.ts';
import { useListView } from '../../lists/useListView.ts';
import { childColumns } from './childColumns.tsx';
import { useT } from '../../../i18n/useT.ts';

/** ChildrenSection is the Subgroups tab: the group's child groups (list id
 * `group-children`, sorted and paged in the browser, never in the URL), each
 * a row to its page. "Subgroup …" sits in the page header. */
export function ChildrenSection({ org, group, groups }: { org: string; group: InventoryGroup; groups: readonly InventoryGroupSummary[] }) {
  const t = useT();
  const children = groups.filter((g) => g.parent_id === group.id);
  const columns = childColumns(t);
  const view = useListView('group-children', columns, { mode: 'client', urlState: false, rowKey: (g) => g.id });
  const { rows, total } = view.view(view.ready ? children : undefined);
  return (
    <div className="flex flex-col gap-3">
      <span className="text-xs text-muted-foreground">{t.inventory.children.intro}</span>
      <DataList
        label={t.inventory.children.label}
        columns={columns}
        rows={rows}
        rowKey={(c) => c.id}
        rowLink={(c) => ({ to: '/o/$org/groups/$id', params: { org, id: c.id }, search: { tab: undefined }, label: c.name })}
        loading={!view.ready}
        empty={t.inventory.children.empty}
        {...view.listProps(total, t.inventory.children.count)}
      />
    </div>
  );
}
