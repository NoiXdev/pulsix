import { Tag } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem, StaticListRow } from '../../ui/list-row.tsx';
import { useT } from '../../i18n/useT.ts';
import type { MonitoringGroup } from '../../api/types.ts';

/** MonitoringGroupRow is one monitoring group. It opens nothing (there is no
 * group page here), and a writer's ⋯ menu offers Edit and Delete…; `columns`
 * reserves the menu's 40px as its last track, left empty here. */
export function MonitoringGroupRow({
  group: g, columns, canWrite, onEdit, onDelete,
}: {
  group: MonitoringGroup;
  columns: string;
  canWrite: boolean;
  onEdit: (g: MonitoringGroup) => void;
  onDelete: (g: MonitoringGroup) => void;
}) {
  const t = useT();
  return (
    <StaticListRow
      columns={columns}
      menu={
        canWrite && (
          <RowMenu label={t.common.rowActions(g.name)}>
            <RowMenuItem onSelect={() => onEdit(g)}>{t.checks.edit}</RowMenuItem>
            <RowMenuItem onSelect={() => onDelete(g)} destructive>
              {t.checks.deleteItem}
            </RowMenuItem>
          </RowMenu>
        )
      }
    >
      <span className="truncate text-[13px] font-medium" data-row>
        {g.name}
      </span>
      <span className="min-w-0">
        <Tag className="truncate">{g.selector}</Tag>
      </span>
      <span className="text-[13px]">{g.priority}</span>
      {canWrite && <span />}
    </StaticListRow>
  );
}
