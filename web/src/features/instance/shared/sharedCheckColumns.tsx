import { CELL_MINOR, type DataListColumn } from '../../../ui/data-list/index.ts';
import type { CheckDefinitionDef } from '../../../api/types.ts';
import type { Messages } from '../../../i18n/types.ts';

/** sharedCheckColumns is every column of the shared check list (list id `shared-checks`). */
export function sharedCheckColumns(t: Messages): DataListColumn<CheckDefinitionDef>[] {
  const c = t.instance.shared.checks;
  return [
    {
      id: 'key',
      label: c.key,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'key',
      sortValue: (r) => r.key,
      required: true,
      defaultVisible: true,
      render: (r) => <span className="block truncate font-mono">{r.key}</span>,
    },
    {
      id: 'plugin',
      label: c.plugin,
      width: 'minmax(0, 0.6fr)',
      sortKey: 'plugin',
      sortValue: (r) => r.plugin,
      defaultVisible: true,
      render: (r) => <span className="block truncate">{r.plugin}</span>,
    },
    {
      id: 'interval',
      label: c.interval,
      width: 'minmax(0, 1fr)',
      sortKey: 'interval',
      sortValue: (r) => r.interval_seconds,
      defaultVisible: true,
      render: (r) => <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{r.interval_seconds}s</span>,
    },
  ];
}
