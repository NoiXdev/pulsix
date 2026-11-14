import { RowMenu, RowMenuItem, StaticListRow } from '../../ui/list-row.tsx';
import type { OrgSecret } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

/** SecretRow is one secret of the store: its name, field names, last
 * change and the checks naming it. It opens nothing; Replace and Delete sit
 * in its ⋯ menu, which only a caller with credentials.write gets. */
export function SecretRow({
  secret,
  columns,
  canWrite,
  onReplace,
  onDelete,
}: {
  secret: OrgSecret;
  columns: string;
  canWrite: boolean;
  onReplace: (s: OrgSecret) => void;
  onDelete: (s: OrgSecret) => void;
}) {
  const t = useT();
  const fmt = useFormat();
  const s = t.secrets;
  const usedBy = secret.used_by ?? [];
  return (
    <StaticListRow
      columns={columns}
      menu={
        canWrite && (
          <RowMenu label={t.common.rowActions(secret.name)}>
            <RowMenuItem onSelect={() => onReplace(secret)}>{s.replace}</RowMenuItem>
            <RowMenuItem onSelect={() => onDelete(secret)} destructive>
              {s.delete}
            </RowMenuItem>
          </RowMenu>
        )
      }
    >
      <span className="truncate font-mono text-[13px] font-medium" data-row>
        {secret.name}
      </span>
      <span className="truncate font-mono text-xs text-muted-foreground">{(secret.field_names ?? []).join(', ')}</span>
      <span className="text-xs text-muted-foreground">{fmt.date(secret.updated_at)}</span>
      <span className="truncate font-mono text-xs">
        {usedBy.length > 0 ? usedBy.join(', ') : <span className="font-sans text-muted-foreground">{s.unused}</span>}
      </span>
      <span />
    </StaticListRow>
  );
}
