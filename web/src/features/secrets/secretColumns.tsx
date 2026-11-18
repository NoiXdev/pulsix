import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { OrgSecret } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';

/**
 * secretColumns is every column the secret store list offers (list id
 * `secrets`), sorted in the browser. A secret opens nothing: Replace and
 * Delete sit in the row menu. A secret's value is never shown, only the
 * names of its fields.
 */
export function secretColumns(t: Messages, fmt: Format): DataListColumn<OrgSecret>[] {
  const c = t.secrets.columns;
  return [
    {
      id: 'name',
      label: c.name,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'name',
      sortValue: (r) => r.name,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-mono font-medium">{r.name}</span>,
    },
    {
      id: 'fields',
      label: c.fields,
      width: 'minmax(0, 1.1fr)',
      sortKey: 'fields',
      sortValue: (r) => (r.field_names ?? []).length,
      defaultVisible: true,
      render: (r) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{(r.field_names ?? []).join(', ')}</span>,
    },
    {
      id: 'updated',
      label: c.updated,
      width: '120px',
      sortKey: 'updated',
      sortValue: (r) => r.updated_at,
      defaultVisible: true,
      render: (r) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{fmt.date(r.updated_at)}</span>,
    },
    {
      id: 'used_by',
      label: c.usedBy,
      width: 'minmax(0, 1fr)',
      sortKey: 'used_by',
      sortValue: (r) => (r.used_by ?? []).length,
      defaultVisible: true,
      render: (r) => {
        const usedBy = r.used_by ?? [];
        return (
          <span className="block truncate font-mono">
            {usedBy.length > 0 ? usedBy.join(', ') : <span className={`font-sans text-muted-foreground ${CELL_MINOR}`}>{t.secrets.unused}</span>}
          </span>
        );
      },
    },
  ];
}
